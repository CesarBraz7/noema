package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const FrameVersion = "yt-dlp-ffmpeg-frame-v1"
const maxFrameBytes = 2 << 20

var ErrFrameUnavailable = errors.New("frame indisponível no trecho solicitado")

type Frame struct {
	AtMS   int64  `json:"atMs"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	JPEG   []byte `json:"-"`
}

// ExtractFrame downloads a short section and samples one frame at the requested offset.
func ExtractFrame(ctx context.Context, root, rawURL, metadataSHA string, atMS, durationMS int64) (Frame, error) {
	id, err := videoID(rawURL)
	if err != nil {
		return Frame{}, err
	}
	if hash, err := hex.DecodeString(metadataSHA); err != nil || len(hash) != sha256.Size || atMS < 0 || atMS >= durationMS {
		return Frame{}, errors.New("origem ou timestamp inválido para extração de frame")
	}
	relative := fmt.Sprintf("youtube/%s/%s/%s/%d.jpg", id, FrameVersion, metadataSHA, atMS)
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return Frame{}, err
	}
	if frame, err := readFrame(target, relative, atMS); err == nil {
		return frame, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Frame{}, err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Frame{}, err
	}
	stage, err := os.MkdirTemp(dir, ".frame-")
	if err != nil {
		return Frame{}, err
	}
	defer os.RemoveAll(stage)

	startMS, endMS := max(0, atMS-500), atMS+min(1000, durationMS-atMS)
	section := "*" + seconds(startMS) + "-" + seconds(endMS)
	cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--no-plugin-dirs", "--no-cache-dir",
		"--use-extractors", "youtube", "--no-playlist", "--no-progress", "--no-simulate", "--match-filters", "!is_live",
		"--socket-timeout", "15", "--retries", "1",
		"--format", "bestvideo[width<=1920][height<=1080][protocol^=http]/best[width<=1920][height<=1080][protocol^=http]",
		"--download-sections", section, "--force-keyframes-at-cuts", "--downloader-args", "ffmpeg:-threads 1 -filter_threads 1",
		"--output", filepath.Join(stage, "clip.%(ext)s"), "--print", "after_move:filepath", "--", watchURL+id)
	cmd.Env = extractorEnv(stage)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return Frame{}, ctx.Err()
		}
		cause := audioDownloadError(err)
		switch {
		case errors.Is(cause, ErrAudioAccess):
			return Frame{}, ErrAudioAccess
		case errors.Is(cause, ErrAudioUnavailable):
			return Frame{}, ErrFrameUnavailable
		default:
			return Frame{}, errors.New("falha ao baixar o trecho visual do YouTube")
		}
	}
	clip := filepath.Clean(strings.TrimSpace(string(output)))
	if filepath.Dir(clip) != stage || !strings.HasPrefix(filepath.Base(clip), "clip.") {
		return Frame{}, ErrFrameUnavailable
	}
	if info, err := os.Lstat(clip); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return Frame{}, ErrFrameUnavailable
	}
	image := filepath.Join(stage, "frame.jpg")
	cmd = exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-threads", "1", "-protocol_whitelist", "file",
		"-ss", seconds(atMS-startMS), "-i", clip, "-map", "0:v:0", "-frames:v", "1", "-c:v", "mjpeg", "-threads", "1", "-q:v", "3", image)
	cmd.Env = extractorEnv(stage)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Frame{}, ctx.Err()
		}
		return Frame{}, ErrFrameUnavailable
	}
	frame, err := readFrame(image, relative, atMS)
	if errors.Is(err, os.ErrNotExist) {
		return Frame{}, ErrFrameUnavailable
	}
	if err != nil {
		return Frame{}, err
	}
	if err := os.Rename(image, target); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func seconds(ms int64) string {
	return strconv.FormatInt(ms/1000, 10) + fmt.Sprintf(".%03d", ms%1000)
}

func readFrame(file, relative string, atMS int64) (Frame, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return Frame{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxFrameBytes {
		return Frame{}, ErrFrameUnavailable
	}
	f, err := os.Open(file)
	if err != nil {
		return Frame{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFrameBytes+1))
	if err != nil {
		return Frame{}, err
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || len(data) > maxFrameBytes || config.Width <= 0 || config.Height <= 0 || config.Width > 1920 || config.Height > 1080 {
		return Frame{}, ErrFrameUnavailable
	}
	hash := sha256.Sum256(data)
	return Frame{AtMS: atMS, File: relative, SHA256: hex.EncodeToString(hash[:]), Size: len(data), Width: config.Width, Height: config.Height, JPEG: data}, nil
}
