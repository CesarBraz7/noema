package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/schemas"
)

const targetedFramesMode = "targeted-frames"

type frameInspection struct {
	SchemaVersion string              `json:"schemaVersion"`
	SourceID      string              `json:"sourceId"`
	Mode          string              `json:"analysisMode"`
	VisualSHA     string              `json:"visualSha256"`
	CaptureSHA    string              `json:"captureSha256"`
	Prompt        string              `json:"promptVersion"`
	Extractor     string              `json:"extractorVersion"`
	CapturedAt    time.Time           `json:"inspectedAt"`
	Frames        []video.Frame       `json:"frames"`
	Missing       []gemini.FrameCheck `json:"missing"`
	Failure       string              `json:"analysisFailure,omitempty"`
	Response      *gemini.Result      `json:"response,omitempty"`
}

func (w *ingestionWorker) captureFrameInspection(ctx context.Context, source sourceContent, native visualRecord, captureRef, visualRef artifact) (frameInspection, artifact, error) {
	key := fingerprint(w.visual.requestVersion(), captureRef.SHA256, visualRef.SHA256)
	location := path.Join("sources", source.SourceID, key, "video-frame-inspection.json")
	cache := filepath.Join(w.visual.workDir, "video-frame-inspection", key+".json")
	template := frameInspection{
		SchemaVersion: schemaVersion, SourceID: source.SourceID, Mode: targetedFramesMode,
		VisualSHA: visualRef.SHA256, CaptureSHA: captureRef.SHA256, Prompt: gemini.FramePromptVersion, Extractor: video.FrameVersion,
	}
	return analysisCheckpoint(w, ctx, source.SourceID, schemas.FrameInspection, location, cache, []string{visualRef.SHA256, captureRef.SHA256},
		func(data []byte) (frameInspection, error) { return w.readFrameInspection(data, template, native) },
		func() (frameInspection, error) { return w.inspectFrames(ctx, template, source, native) })
}

func (w *ingestionWorker) inspectFrames(ctx context.Context, out frameInspection, source sourceContent, native visualRecord) (frameInspection, error) {
	out.Frames = []video.Frame{}
	out.Missing = []gemini.FrameCheck{}
	var inputs []gemini.FrameInput
	seen := make(map[int64]bool)
	var restricted bool
	for _, check := range native.Response.FrameChecks {
		atMS := *check.AtMS
		if seen[atMS] {
			continue
		}
		seen[atMS] = true
		if restricted {
			out.Missing = append(out.Missing, gemini.FrameCheck{AtMS: check.AtMS, Reason: video.ErrAudioAccess.Error()})
			continue
		}
		frame, err := w.visual.extractFrame(ctx, w.visual.workDir, native.URL, native.MetadataSHA, atMS, native.DurationMS)
		switch {
		case errors.Is(err, video.ErrFrameUnavailable), errors.Is(err, video.ErrAudioAccess):
			restricted = errors.Is(err, video.ErrAudioAccess)
			out.Missing = append(out.Missing, gemini.FrameCheck{AtMS: check.AtMS, Reason: err.Error()})
		case err != nil:
			return frameInspection{}, err
		default:
			inputs = append(inputs, gemini.FrameInput{Frame: frame, Reason: check.Reason, Transcript: nearbyTranscript(source, atMS)})
			frame.JPEG = nil
			out.Frames = append(out.Frames, frame)
		}
	}
	if len(inputs) != 0 && restricted {
		out.Failure = "A inspeção foi interrompida após uma restrição de acesso ao vídeo."
	} else if len(inputs) != 0 {
		response, err := w.visual.frameClient.AnalyzeFrames(ctx, inputs, native.DurationMS)
		switch {
		case errors.Is(err, gemini.ErrAuthorization):
			return frameInspection{}, fmt.Errorf("%w: %w", errReview, err)
		case errors.Is(err, gemini.ErrRejected):
			out.Failure = "O Gemini não aceitou a inspeção dos frames extraídos."
		case err != nil:
			return frameInspection{}, err
		default:
			for i := range response.Observations {
				response.Observations[i].ID = fmt.Sprintf("f-%03d", i+1)
			}
			out.Response = &response
		}
	}
	out.CapturedAt = time.Now().UTC().Truncate(time.Second)
	return out, nil
}

func nearbyTranscript(source sourceContent, atMS int64) string {
	if source.Capture.Method != "whisper.cpp" {
		return ""
	}
	const windowMS, maxChars = 8000, 800
	var text strings.Builder
	var written int
	for _, block := range source.Blocks {
		if block.StartMS == nil || block.EndMS == nil || *block.EndMS < atMS-windowMS || *block.StartMS-atMS > windowMS {
			continue
		}
		excerpt := []rune(strings.Join(strings.Fields(block.Text), " "))
		excerpt = excerpt[:min(len(excerpt), maxChars-written)]
		written += len(excerpt) + 1
		text.WriteString(string(excerpt))
		if written >= maxChars {
			break
		}
		text.WriteByte(' ')
	}
	return strings.TrimSpace(text.String())
}

func (w *ingestionWorker) readFrameInspection(data []byte, expected frameInspection, native visualRecord) (frameInspection, error) {
	var actual frameInspection
	invalid := fmt.Errorf("%w: checkpoint de inspeção de frames inválido", errReview)
	if w.validator.Validate(schemas.FrameInspection, data) != nil || json.Unmarshal(data, &actual) != nil ||
		actual.SourceID != expected.SourceID || actual.Mode != expected.Mode || actual.VisualSHA != expected.VisualSHA ||
		actual.CaptureSHA != expected.CaptureSHA || actual.Prompt != expected.Prompt || actual.Extractor != expected.Extractor {
		return frameInspection{}, invalid
	}
	pending := make(map[int64]bool)
	for _, check := range native.Response.FrameChecks {
		pending[*check.AtMS] = true
	}
	timestamps := make(map[int64]bool, len(actual.Frames))
	for _, frame := range actual.Frames {
		if !pending[frame.AtMS] {
			return frameInspection{}, invalid
		}
		delete(pending, frame.AtMS)
		timestamps[frame.AtMS] = true
	}
	for _, check := range actual.Missing {
		if check.AtMS == nil || !pending[*check.AtMS] {
			return frameInspection{}, invalid
		}
		delete(pending, *check.AtMS)
	}
	if len(pending) != 0 {
		return frameInspection{}, invalid
	}
	if actual.Response != nil {
		if len(actual.Frames) == 0 || actual.Failure != "" || !actual.Response.Valid(native.DurationMS) ||
			len(actual.Response.FrameChecks) != 0 || !actual.Response.MatchesFrames(timestamps) {
			return frameInspection{}, invalid
		}
		for i, observation := range actual.Response.Observations {
			if observation.ID != fmt.Sprintf("f-%03d", i+1) {
				return frameInspection{}, invalid
			}
		}
	} else if len(actual.Frames) != 0 && actual.Failure == "" {
		return frameInspection{}, invalid
	}
	return actual, nil
}
