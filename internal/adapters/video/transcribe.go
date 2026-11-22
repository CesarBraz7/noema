package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	WhisperModelPath = "/opt/noema-models/ggml-tiny-q5_1.bin"
	WhisperModel     = "tiny-q5_1"
	WhisperVersion   = "1.9.5"
	maxAudioSeconds  = 20 * 60
	maxAudioBytes    = 64 << 20
)

var (
	ErrAudioAccess      = errors.New("YouTube restringiu o acesso ao áudio")
	ErrAudioUnavailable = errors.New("áudio indisponível para transcrição local")
	ErrNoSpeech         = errors.New("transcrição sem trechos de fala com timestamps")
)

type Transcript struct {
	Language string
	Segments []Segment
}

// Transcribe downloads one audio track, normalizes it to mono PCM and runs Whisper locally.
func Transcribe(ctx context.Context, capture Capture) (Transcript, error) {
	if capture.Duration <= 0 || capture.Duration > maxAudioSeconds {
		return Transcript{}, fmt.Errorf("%w: a duração precisa estar entre 0 e %d minutos", ErrAudioUnavailable, maxAudioSeconds/60)
	}
	canonical, err := CanonicalURL(capture.URL)
	if err != nil {
		return Transcript{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "noema-audio-")
	if err != nil {
		return Transcript{}, fmt.Errorf("preparar diretório temporário de áudio: %w", err)
	}
	defer os.RemoveAll(dir)

	cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--no-plugin-dirs", "--no-cache-dir",
		"--use-extractors", "youtube", "--no-playlist", "--no-progress", "--no-part",
		"--format", "bestaudio", "--max-filesize", "64M",
		"--match-filters", fmt.Sprintf("duration <= %d & !is_live", maxAudioSeconds),
		"--socket-timeout", "15", "--retries", "1", "--fragment-retries", "1",
		"--output", filepath.Join(dir, "audio.%(ext)s"), "--", canonical)
	cmd.Env = extractorEnv(dir)
	if _, err := cmd.Output(); err != nil {
		if ctx.Err() != nil {
			return Transcript{}, ctx.Err()
		}
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			message := strings.ToLower(string(failure.Stderr))
			switch {
			case strings.Contains(message, "http error 403"), strings.Contains(message, "sign in"), strings.Contains(message, "private video"):
				return Transcript{}, ErrAudioAccess
			case strings.Contains(message, "requested format is not available"), strings.Contains(message, "video unavailable"):
				return Transcript{}, ErrAudioUnavailable
			}
		}
		return Transcript{}, errors.New("falha ao baixar o áudio do YouTube")
	}
	files, err := filepath.Glob(filepath.Join(dir, "audio.*"))
	if err != nil || len(files) != 1 {
		return Transcript{}, ErrAudioUnavailable
	}
	audio, err := os.Stat(files[0])
	if err != nil || !audio.Mode().IsRegular() || audio.Size() == 0 || audio.Size() > maxAudioBytes {
		return Transcript{}, ErrAudioUnavailable
	}

	wav := filepath.Join(dir, "mono.wav")
	cmd = exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
		"-i", files[0], "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le",
		"-t", fmt.Sprint(maxAudioSeconds), wav)
	cmd.Env = extractorEnv(dir)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Transcript{}, ctx.Err()
		}
		return Transcript{}, errors.New("FFmpeg não conseguiu normalizar o áudio")
	}

	output := filepath.Join(dir, "transcript")
	cmd = exec.CommandContext(ctx, "whisper-cli", "-m", WhisperModelPath, "-f", wav,
		"-l", "auto", "-t", "1", "-ng", "-np", "-oj", "-of", output)
	cmd.Env = extractorEnv(dir)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Transcript{}, ctx.Err()
		}
		return Transcript{}, errors.New("Whisper não conseguiu transcrever o áudio")
	}
	file, err := os.Open(output + ".json")
	if err != nil {
		return Transcript{}, errors.New("Whisper não gerou a transcrição JSON")
	}
	defer file.Close()
	return parseTranscript(file)
}

func parseTranscript(reader io.Reader) (Transcript, error) {
	const maxJSON = 4 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maxJSON+1))
	if err != nil || len(data) > maxJSON {
		return Transcript{}, errors.New("falha ao ler o JSON do Whisper")
	}
	var payload struct {
		Result struct {
			Language string `json:"language"`
		} `json:"result"`
		Transcription []struct {
			Text    string `json:"text"`
			Offsets struct {
				From *int64 `json:"from"`
				To   *int64 `json:"to"`
			} `json:"offsets"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Result.Language == "" {
		return Transcript{}, errors.New("JSON do Whisper sem idioma ou segmentos válidos")
	}
	transcript := Transcript{Language: payload.Result.Language}
	for _, part := range payload.Transcription {
		start, end := part.Offsets.From, part.Offsets.To
		text := strings.TrimSpace(part.Text)
		if start == nil || end == nil || *start < 0 || *end <= *start || text == "" {
			continue
		}
		switch strings.ToLower(text) {
		case "[blank_audio]", "[music]", "(music)", "[silence]", "(silence)", "[inaudible]", "(inaudible)":
			continue
		}
		transcript.Segments = append(transcript.Segments, Segment{StartMS: *start, DurationMS: *end - *start, Text: text})
	}
	if len(transcript.Segments) == 0 {
		return Transcript{}, ErrNoSpeech
	}
	return transcript, nil
}
