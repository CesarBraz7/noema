package video

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type audioSnapshot struct {
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type audioPart struct {
	file    string
	startMS int64
	endMS   int64
}

func originalAudio(ctx context.Context, dir, canonical string) (audioSnapshot, error) {
	index := filepath.Join(dir, "audio.json")
	data, err := os.ReadFile(index)
	if err == nil {
		var audio audioSnapshot
		if err := json.Unmarshal(data, &audio); err != nil || !isAudioFilename(audio.File) {
			return audioSnapshot{}, errors.New("checkpoint do áudio original inválido")
		}
		if hash, err := hex.DecodeString(audio.SHA256); err != nil || len(hash) != sha256.Size {
			return audioSnapshot{}, errors.New("hash inválido no checkpoint do áudio original")
		}
		file := filepath.Join(dir, audio.File)
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() != audio.Size {
			return audioSnapshot{}, errors.New("arquivo de áudio original ausente ou alterado")
		}
		if info.ModTime().UnixNano() != audio.Modified {
			sha, err := fileSHA(file)
			if err != nil || sha != audio.SHA256 {
				return audioSnapshot{}, errors.New("hash do áudio original diverge do checkpoint")
			}
		}
		return audio, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return audioSnapshot{}, err
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--no-plugin-dirs", "--no-cache-dir",
		"--use-extractors", "youtube", "--no-playlist", "--no-progress", "--no-simulate",
		"--format", "bestaudio/best[acodec!=none]", "--match-filters", "!is_live",
		"--socket-timeout", "15", "--retries", "1", "--fragment-retries", "1",
		"--output", filepath.Join(dir, "original.%(ext)s"), "--print", "after_move:filepath", "--", canonical)
	cmd.Env = extractorEnv(dir)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return audioSnapshot{}, ctx.Err()
		}
		return audioSnapshot{}, audioDownloadError(err)
	}
	file := filepath.Clean(strings.TrimSpace(string(output)))
	if filepath.Dir(file) != dir || !isAudioFilename(filepath.Base(file)) {
		return audioSnapshot{}, ErrAudioUnavailable
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return audioSnapshot{}, ErrAudioUnavailable
	}
	sha, err := fileSHA(file)
	if err != nil {
		return audioSnapshot{}, err
	}
	audio := audioSnapshot{File: filepath.Base(file), SHA256: sha, Size: info.Size(), Modified: info.ModTime().UnixNano()}
	if err := writeCheckpoint(index, audio); err != nil {
		return audioSnapshot{}, err
	}
	return audio, nil
}

func isAudioFilename(name string) bool {
	return name == filepath.Base(name) && strings.HasPrefix(name, "original.") && !strings.ContainsAny(name, "/\\\r\n")
}

func audioDownloadError(err error) error {
	var failure *exec.ExitError
	if errors.As(err, &failure) {
		message := strings.ToLower(string(failure.Stderr))
		switch {
		case strings.Contains(message, "http error 401"), strings.Contains(message, "http error 403"),
			strings.Contains(message, "sign in"), strings.Contains(message, "private video"),
			strings.Contains(message, "not available in your country"), strings.Contains(message, "geo-restricted"):
			return ErrAudioAccess
		case strings.Contains(message, "requested format is not available"), strings.Contains(message, "video unavailable"):
			return ErrAudioUnavailable
		}
	}
	return errors.New("falha ao baixar o áudio do YouTube")
}

func normalizeAudio(ctx context.Context, dir string, audio audioSnapshot, window time.Duration) (string, []audioPart, error) {
	key := sha256.Sum256([]byte(fmt.Sprintf("pcm-v1/%s/%s", audio.SHA256, window)))
	pcm := filepath.Join(dir, fmt.Sprintf("pcm-%x", key))
	index := filepath.Join(pcm, "parts.csv")
	parts, err := readAudioParts(index)
	if err == nil {
		return pcm, parts, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	stage, err := os.MkdirTemp(dir, ".pcm-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(stage)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file",
		"-i", filepath.Join(dir, audio.File), "-map", "0:a:0", "-vn", "-map_metadata", "-1",
		"-af", "aresample=async=1:first_pts=0", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le",
		"-f", "segment", "-segment_time", strconv.FormatFloat(window.Seconds(), 'f', -1, 64),
		"-segment_format", "wav", "-reset_timestamps", "1", "-segment_list", "parts.csv",
		"-segment_list_type", "csv", "part-%06d.wav")
	cmd.Dir = stage
	cmd.Env = extractorEnv(stage)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, errors.New("FFmpeg não conseguiu dividir o áudio original")
	}
	parts, err = readAudioParts(filepath.Join(stage, "parts.csv"))
	if err != nil {
		return "", nil, err
	}
	if err := os.Rename(stage, pcm); err != nil {
		return "", nil, err
	}
	return pcm, parts, nil
}

func readAudioParts(index string) ([]audioPart, error) {
	file, err := os.Open(index)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = 3
	var parts []audioPart
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		start, errStart := strconv.ParseFloat(row[1], 64)
		end, errEnd := strconv.ParseFloat(row[2], 64)
		if row[0] != fmt.Sprintf("part-%06d.wav", len(parts)) || errStart != nil || errEnd != nil ||
			!(start >= 0 && end > start && end < float64(math.MaxInt64)/1000) {
			return nil, errors.New("segmento de áudio com nome ou intervalo inválido")
		}
		part := audioPart{file: row[0], startMS: int64(math.Round(start * 1000)), endMS: int64(math.Round(end * 1000))}
		if part.endMS <= part.startMS || len(parts) > 0 && part.startMS < parts[len(parts)-1].endMS {
			return nil, errors.New("intervalos de áudio inválidos no índice do FFmpeg")
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return nil, ErrNoSpeech
	}
	return parts, nil
}

func fileSHA(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeCheckpoint(path string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(value)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
