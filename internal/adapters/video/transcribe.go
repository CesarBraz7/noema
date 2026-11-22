package video

import (
	"context"
	"crypto/sha256"
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
	WhisperVersion   = "1.9.5"
)

var (
	ErrAudioAccess      = errors.New("YouTube restringiu o acesso ao áudio")
	ErrAudioUnavailable = errors.New("áudio indisponível para transcrição local")
	ErrNoSpeech         = errors.New("transcrição sem trechos de fala com timestamps")
	ErrMoreAudio        = errors.New("transcrição de áudio em andamento")
)

type AudioEvidence struct {
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	StartMS  int64  `json:"startMs"`
	EndMS    int64  `json:"endMs"`
	Parts    int    `json:"parts"`
	NoSpeech int    `json:"partsWithoutRecognizedSpeech,omitempty"`
}

type Transcript struct {
	Language string         `json:"language"`
	Segments []Segment      `json:"segments"`
	Model    string         `json:"model,omitempty"`
	ModelSHA string         `json:"modelSha256,omitempty"`
	Audio    *AudioEvidence `json:"audio,omitempty"`
}

type Transcriber struct {
	root      string
	modelPath string
	modelSHA  string
	window    time.Duration
}

func NewTranscriber(root, modelPath string, window time.Duration) (*Transcriber, error) {
	if window <= 0 {
		return nil, errors.New("a duração do segmento de áudio precisa ser positiva")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	modelPath, err = filepath.Abs(modelPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(modelPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("modelo Whisper vazio ou inválido")
	}
	modelSHA, err := fileSHA(modelPath)
	if err != nil {
		return nil, err
	}
	return &Transcriber{root: root, modelPath: modelPath, modelSHA: modelSHA, window: window}, nil
}

func (t *Transcriber) Version() string {
	return fmt.Sprintf("whisper.cpp-%s/%s/%s", WhisperVersion, t.modelSHA, t.window)
}

// Transcribe advances the first unfinished audio segment. A complete transcript
// is returned after every segment has a checkpoint.
func (t *Transcriber) Transcribe(ctx context.Context, capture Capture) (Transcript, error) {
	id, err := videoID(capture.URL)
	if err != nil {
		return Transcript{}, err
	}
	workspace := filepath.Join(t.root, "youtube", id)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return Transcript{}, fmt.Errorf("preparar armazenamento de áudio: %w", err)
	}
	audio, err := originalAudio(ctx, workspace, watchURL+id)
	if err != nil {
		return Transcript{}, err
	}
	pcm, parts, err := normalizeAudio(ctx, workspace, audio, t.window)
	if err != nil {
		return Transcript{}, err
	}

	key := sha256.Sum256([]byte(audio.SHA256 + "/" + t.Version()))
	checkpoints := filepath.Join(workspace, fmt.Sprintf("asr-%x", key))
	if err := os.MkdirAll(checkpoints, 0700); err != nil {
		return Transcript{}, err
	}
	for i, part := range parts {
		file := filepath.Join(checkpoints, fmt.Sprintf("%06d.json", i))
		if _, err := os.Stat(file); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return Transcript{}, err
		}
		chunk, err := t.recognize(ctx, filepath.Join(pcm, part.file))
		if err != nil && !errors.Is(err, ErrNoSpeech) {
			return Transcript{}, err
		}
		if err := writeCheckpoint(file, chunk); err != nil {
			return Transcript{}, err
		}
		if i+1 < len(parts) {
			return Transcript{}, fmt.Errorf("%w: %d de %d segmentos", ErrMoreAudio, i+1, len(parts))
		}
	}
	return t.joinTranscript(checkpoints, audio, parts, id)
}

func (t *Transcriber) joinTranscript(checkpoints string, audio audioSnapshot, parts []audioPart, id string) (Transcript, error) {
	transcript := Transcript{
		Model: strings.TrimSuffix(filepath.Base(t.modelPath), filepath.Ext(t.modelPath)), ModelSHA: t.modelSHA,
		Audio: &AudioEvidence{File: "youtube/" + id + "/" + audio.File, SHA256: audio.SHA256, Size: audio.Size,
			StartMS: parts[0].startMS, EndMS: parts[len(parts)-1].endMS, Parts: len(parts)},
	}
	for i, part := range parts {
		data, err := os.ReadFile(filepath.Join(checkpoints, fmt.Sprintf("%06d.json", i)))
		if err != nil {
			return Transcript{}, err
		}
		var chunk Transcript
		if err := json.Unmarshal(data, &chunk); err != nil {
			return Transcript{}, fmt.Errorf("checkpoint de transcrição inválido no segmento %d", i+1)
		}
		if len(chunk.Segments) == 0 {
			transcript.Audio.NoSpeech++
			continue
		}
		if transcript.Language == "" {
			transcript.Language = chunk.Language
		} else if transcript.Language != chunk.Language {
			transcript.Language = "und"
		}
		for _, segment := range chunk.Segments {
			if segment.StartMS < 0 || segment.DurationMS <= 0 || strings.TrimSpace(segment.Text) == "" {
				return Transcript{}, fmt.Errorf("trecho inválido no checkpoint do segmento %d", i+1)
			}
			windowMS := part.endMS - part.startMS
			if segment.StartMS >= windowMS {
				continue
			}
			segment.DurationMS = min(segment.DurationMS, windowMS-segment.StartMS)
			segment.StartMS += part.startMS
			transcript.Segments = append(transcript.Segments, segment)
		}
	}
	if len(transcript.Segments) == 0 {
		return Transcript{}, ErrNoSpeech
	}
	return transcript, nil
}

func (t *Transcriber) recognize(ctx context.Context, wav string) (Transcript, error) {
	dir, err := os.MkdirTemp(filepath.Dir(wav), ".whisper-")
	if err != nil {
		return Transcript{}, err
	}
	defer os.RemoveAll(dir)

	output := filepath.Join(dir, "transcript")
	cmd := exec.CommandContext(ctx, "whisper-cli", "-m", t.modelPath, "-f", wav,
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
