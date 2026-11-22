// Package video inspects public YouTube videos and retrieves a timed caption track.
package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

var youtubeID = regexp.MustCompile("^[A-Za-z0-9_-]{11}$")

// Version identifies the caption extractor and its output contract.
const Version = "yt-dlp-json3-v1"

const watchURL = "https://www.youtube.com/watch?v="

const (
	CaptionsUnavailable = "unavailable"
	CaptionsFetchFailed = "fetch_failed"
	CaptionsAvailable   = "available"
)

type Capture struct {
	ID             string        `json:"id"`
	URL            string        `json:"url"`
	Title          string        `json:"title"`
	Channel        string        `json:"channel,omitempty"`
	Duration       float64       `json:"durationSeconds,omitempty"`
	PublishedAt    string        `json:"publishedAt,omitempty"`
	CaptionsStatus string        `json:"captionsStatus"`
	Captions       *CaptionTrack `json:"captions,omitempty"`
}

type CaptionTrack struct {
	Language  string    `json:"language"`
	Automatic bool      `json:"automatic"`
	Segments  []Segment `json:"segments"`
}

type Segment struct {
	StartMS    int64  `json:"startMs"`
	DurationMS int64  `json:"durationMs"`
	Text       string `json:"text"`
}

type subtitleFormat struct {
	Ext string `json:"ext"`
	URL string `json:"url"`
}

type metadata struct {
	ID                string                      `json:"id"`
	Title             string                      `json:"title"`
	Channel           string                      `json:"channel"`
	Uploader          string                      `json:"uploader"`
	UploadDate        string                      `json:"upload_date"`
	Duration          float64                     `json:"duration"`
	Language          string                      `json:"language"`
	Subtitles         map[string][]subtitleFormat `json:"subtitles"`
	AutomaticCaptions map[string][]subtitleFormat `json:"automatic_captions"`
}

func Inspect(ctx context.Context, raw string) (Capture, error) {
	id, err := videoID(raw)
	if err != nil {
		return Capture{}, err
	}
	canonical := watchURL + id
	extractCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	cache, err := os.MkdirTemp("", "noema-video-")
	if err != nil {
		return Capture{}, fmt.Errorf("preparar diretório temporário para yt-dlp: %w", err)
	}
	defer os.RemoveAll(cache)
	cmd := exec.CommandContext(extractCtx, "yt-dlp", "--ignore-config", "--no-plugin-dirs", "--no-cache-dir",
		"--use-extractors", "youtube", "--no-playlist", "--skip-download", "--ignore-no-formats-error",
		"--socket-timeout", "15", "--retries", "1",
		"--print", "%(.{id,title,channel,uploader,upload_date,duration,language,subtitles,automatic_captions})j", "--", canonical)
	cmd.Env = extractorEnv(cache)
	output, err := cmd.Output()
	switch {
	case extractCtx.Err() != nil:
		return Capture{}, extractCtx.Err()
	case errors.Is(err, exec.ErrNotFound):
		return Capture{}, errors.New("instale yt-dlp no PATH ou use a imagem Docker do Noema")
	case err != nil:
		return Capture{}, errors.New("yt-dlp não conseguiu obter os metadados do vídeo público")
	}
	var meta metadata
	if err := json.Unmarshal(output, &meta); err != nil || meta.ID != id || strings.TrimSpace(meta.Title) == "" {
		return Capture{}, errors.New("yt-dlp retornou metadados incompletos do vídeo")
	}
	capture := Capture{
		ID: id, URL: canonical, Title: meta.Title, Channel: meta.Channel, Duration: meta.Duration,
		CaptionsStatus: CaptionsUnavailable,
	}
	if capture.Channel == "" {
		capture.Channel = meta.Uploader
	}
	if date, err := time.Parse("20060102", meta.UploadDate); err == nil {
		capture.PublishedAt = date.Format(time.DateOnly)
	}
	target, track := chooseTrack(meta)
	if target == "" {
		return capture, nil
	}
	track.Segments, err = fetchSegments(ctx, target)
	if ctx.Err() != nil {
		return Capture{}, ctx.Err()
	}
	if err != nil {
		capture.CaptionsStatus = CaptionsFetchFailed
		return capture, nil
	}
	capture.CaptionsStatus = CaptionsAvailable
	capture.Captions = &track
	return capture, nil
}

func extractorEnv(cache string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "PYTHONUTF8=1",
		"DENO_DIR=" + cache, "TMPDIR=" + cache, "TEMP=" + cache,
	}
}

func CanonicalURL(raw string) (string, error) {
	id, err := videoID(raw)
	if err != nil {
		return "", err
	}
	return watchURL + id, nil
}

func videoID(raw string) (string, error) {
	invalid := errors.New("informe a URL HTTPS de um vídeo individual do YouTube")
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return "", invalid
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	var id string
	switch strings.ToLower(parsed.Host) {
	case "youtu.be":
		if len(parts) == 1 {
			id = parts[0]
		}
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		if parsed.Path == "/watch" {
			ids := parsed.Query()["v"]
			if len(ids) == 1 {
				id = ids[0]
			}
		} else if len(parts) == 2 && slices.Contains([]string{"shorts", "live", "embed"}, parts[0]) {
			id = parts[1]
		}
	}
	if !youtubeID.MatchString(id) {
		return "", invalid
	}
	return id, nil
}

func chooseTrack(meta metadata) (string, CaptionTrack) {
	for _, automatic := range []bool{false, true} {
		formats := meta.Subtitles
		if automatic {
			formats = meta.AutomaticCaptions
		}
		languages := make([]string, 0, len(formats))
		for language := range formats {
			languages = append(languages, language)
		}
		slices.Sort(languages)
		preferred := []string{meta.Language + "-orig", meta.Language, "pt-BR", "pt", "en"}
		for _, language := range append(preferred, languages...) {
			for _, format := range formats[language] {
				if format.Ext == "json3" && validCaptionURL(format.URL, automatic) {
					return format.URL, CaptionTrack{Language: strings.TrimSuffix(language, "-orig"), Automatic: automatic}
				}
			}
		}
	}
	return "", CaptionTrack{}
}

func validCaptionURL(raw string, automatic bool) bool {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.User != nil || target.Path != "/api/timedtext" {
		return false
	}
	switch target.Host {
	case "www.youtube.com", "youtube.com", "m.youtube.com":
		return !automatic || !target.Query().Has("tlang")
	default:
		return false
	}
}

func fetchSegments(ctx context.Context, target string) ([]Segment, error) {
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("YouTube respondeu com HTTP %d", response.StatusCode)
	}
	return parseSegments(response.Body)
}

func parseSegments(reader io.Reader) ([]Segment, error) {
	const maxCaptionBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maxCaptionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCaptionBytes {
		return nil, errors.New("legenda excede o limite de tamanho")
	}
	var payload struct {
		Events []struct {
			Start    *int64 `json:"tStartMs"`
			Duration int64  `json:"dDurationMs"`
			Parts    []struct {
				Text string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	var segments []Segment
	for _, event := range payload.Events {
		if event.Start == nil || *event.Start < 0 || event.Duration < 0 || event.Duration > math.MaxInt64-*event.Start {
			continue
		}
		var text strings.Builder
		for _, part := range event.Parts {
			text.WriteString(part.Text)
		}
		if clean := strings.TrimSpace(text.String()); clean != "" {
			segments = append(segments, Segment{StartMS: *event.Start, DurationMS: event.Duration, Text: clean})
		}
	}
	if len(segments) == 0 {
		return nil, errors.New("legenda sem segmentos com timestamps")
	}
	return segments, nil
}
