package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/internal/storage/local"
	"github.com/CesarBraz7/noema/schemas"
)

const (
	visualURLMode             = "youtube-url"
	missingVisualConfigReview = "configuração do Gemini mudou antes da análise visual"
)

type visualAnalyzer interface {
	AnalyzeVideo(context.Context, string, int64) (gemini.Result, error)
}

type frameAnalyzer interface {
	AnalyzeFrames(context.Context, []gemini.FrameInput, int64) (gemini.Result, error)
}

type visualStage struct {
	model        string
	workDir      string
	client       visualAnalyzer
	frameClient  frameAnalyzer
	extractFrame func(context.Context, string, string, string, int64, int64) (video.Frame, error)
}

func visualFromEnv() (*visualStage, error) {
	key, model := os.Getenv("GEMINI_API_KEY"), os.Getenv("NOEMA_GEMINI_MODEL")
	// Selecting a model explicitly enables this stage for the dedicated free-tier project.
	if model == "" {
		return nil, nil
	}
	client, err := gemini.New(key, model)
	if err != nil {
		return nil, err
	}
	return &visualStage{
		model: model, workDir: envDefault("NOEMA_WORK_DIR", "data"), client: client,
		frameClient: client, extractFrame: video.ExtractFrame,
	}, nil
}

func (v *visualStage) version() string {
	if v == nil {
		return ""
	}
	return fingerprint("gemini", gemini.PromptVersion, visualURLMode, v.model)
}

func (v *visualStage) inspectionVersion() string {
	native := v.version()
	if v == nil || v.frameClient == nil || v.extractFrame == nil {
		return native
	}
	return fingerprint(native, gemini.FramePromptVersion, video.FrameVersion)
}

func (v *visualStage) requestVersion() string {
	inspection := v.inspectionVersion()
	if inspection == v.version() {
		return inspection
	}
	return fingerprint(inspection, fallbackFramesMode)
}

type visualRecord struct {
	SchemaVersion  string         `json:"schemaVersion"`
	SourceID       string         `json:"sourceId"`
	URL            string         `json:"sourceUrl"`
	Provider       string         `json:"provider"`
	Mode           string         `json:"analysisMode"`
	Prompt         string         `json:"promptVersion"`
	RequestedModel string         `json:"requestedModel"`
	MetadataSHA    string         `json:"metadataSha256"`
	DurationMS     int64          `json:"durationMs"`
	CapturedAt     time.Time      `json:"analyzedAt"`
	BudgetUSD      int            `json:"budgetUsd"`
	Response       *gemini.Result `json:"response,omitempty"`
	Unavailable    string         `json:"nativeUnavailable,omitempty"`
}

func (w *ingestionWorker) captureVisual(ctx context.Context, req request, source sourceContent, captureRef artifact, root string) (visualRecord, artifact, error) {
	if w.visual.version() != req.visualVersion && w.visual.inspectionVersion() != req.visualVersion &&
		w.visual.requestVersion() != req.visualVersion {
		return visualRecord{}, artifact{}, fmt.Errorf("%w: %s", errReview, missingVisualConfigReview)
	}
	if len(captureRef.Dependencies) == 0 {
		return visualRecord{}, artifact{}, fmt.Errorf("%w: captura de vídeo sem referência para os metadados", errReview)
	}
	duration, durationMS := source.Source.DurationSeconds, int64(0)
	if duration > 0 && duration < float64(math.MaxInt64)/1000 {
		durationMS = int64(math.Round(duration * 1000))
	} else if source.Capture.Audio != nil {
		durationMS = source.Capture.Audio.EndMS
	}
	if durationMS <= 0 {
		return visualRecord{}, artifact{}, fmt.Errorf("%w: duração do vídeo indisponível para validar timestamps visuais", errReview)
	}
	if err := w.state(ctx, req.id, "ANALYZING", ""); err != nil {
		return visualRecord{}, artifact{}, err
	}
	metadataSHA := captureRef.Dependencies[0]
	key := fingerprint(w.visual.version(), metadataSHA, strconv.FormatInt(durationMS, 10))
	location := path.Join(root, key, "video-visual.json")
	template := visualRecord{
		SchemaVersion: schemaVersion, SourceID: req.sourceID, URL: req.url, Provider: "gemini", Mode: visualURLMode,
		Prompt: gemini.PromptVersion, RequestedModel: w.visual.model, MetadataSHA: metadataSHA, DurationMS: durationMS,
	}
	cache := filepath.Join(w.visual.workDir, "video-visual", key+".json")
	return analysisCheckpoint(w, ctx, req.sourceID, schemas.Visual, location, cache, []string{metadataSHA},
		func(data []byte) (visualRecord, error) { return w.readVisualRecord(data, template) },
		func() (visualRecord, error) { return w.analyzeVisualRecord(ctx, template) })
}

func (w *ingestionWorker) analyzeVisualRecord(ctx context.Context, template visualRecord) (visualRecord, error) {
	response, err := w.visual.client.AnalyzeVideo(ctx, template.URL, template.DurationMS)
	template.CapturedAt = time.Now().UTC().Truncate(time.Second)
	if errors.Is(err, gemini.ErrNativeVideo) {
		template.Unavailable = gemini.ErrNativeVideo.Error()
		return template, nil
	}
	if errors.Is(err, gemini.ErrAuthorization) || errors.Is(err, gemini.ErrRejected) {
		return visualRecord{}, fmt.Errorf("%w: %w", errReview, err)
	}
	if err != nil {
		return visualRecord{}, err
	}
	for i := range response.Observations {
		response.Observations[i].ID = fmt.Sprintf("v-%03d", i+1)
	}
	template.Response = &response
	return template, nil
}

func (w *ingestionWorker) readVisualRecord(data []byte, expected visualRecord) (visualRecord, error) {
	var actual visualRecord
	if w.validator.Validate(schemas.Visual, data) != nil || json.Unmarshal(data, &actual) != nil ||
		actual.SourceID != expected.SourceID || actual.URL != expected.URL || actual.MetadataSHA != expected.MetadataSHA ||
		actual.Mode != expected.Mode || actual.Prompt != expected.Prompt || actual.RequestedModel != expected.RequestedModel ||
		actual.DurationMS != expected.DurationMS {
		return visualRecord{}, fmt.Errorf("%w: checkpoint de análise visual inválido", errReview)
	}
	if actual.Response == nil {
		if actual.Unavailable != gemini.ErrNativeVideo.Error() {
			return visualRecord{}, fmt.Errorf("%w: rejeição de vídeo sem diagnóstico reconhecido", errReview)
		}
		return actual, nil
	}
	if !actual.Response.Valid(actual.DurationMS) {
		return visualRecord{}, fmt.Errorf("%w: evidências visuais inválidas", errReview)
	}
	for i, observation := range actual.Response.Observations {
		if observation.ID != fmt.Sprintf("v-%03d", i+1) {
			return visualRecord{}, fmt.Errorf("%w: evidência visual sem identificador estável", errReview)
		}
	}
	return actual, nil
}

func analysisCheckpoint[T any](w *ingestionWorker, ctx context.Context, sourceID, kind, location, cache string, dependencies []string,
	read func([]byte) (T, error), analyze func() (T, error),
) (T, artifact, error) {
	var record T
	file, err := w.artifacts.Get(ctx, location, "")
	if err != nil {
		return record, artifact{}, err
	}
	if file.Exists {
		record, err = read(file.Data)
		if err != nil {
			return record, artifact{}, err
		}
		ref, err := w.recordArtifact(ctx, sourceID, kind, location, file.Data, dependencies)
		return record, ref, err
	}
	var known bool
	if err := w.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM noema_artifacts WHERE path = $1)`, location).Scan(&known); err != nil {
		return record, artifact{}, err
	}
	if known {
		return record, artifact{}, fmt.Errorf("%w: checkpoint de %s ausente", errReview, kind)
	}

	data, err := os.ReadFile(cache)
	missing := errors.Is(err, os.ErrNotExist)
	if missing {
		record, err = analyze()
		if err != nil {
			return record, artifact{}, err
		}
		data, err = encodeArtifact(record)
		if err != nil {
			return record, artifact{}, err
		}
	} else if err != nil {
		return record, artifact{}, err
	}
	record, err = read(data)
	if err != nil {
		return record, artifact{}, err
	}
	if missing {
		if err := local.PutJSON(cache, record); err != nil {
			return record, artifact{}, err
		}
	}
	ref, err := w.putArtifact(ctx, sourceID, kind, location, record, dependencies)
	return record, ref, err
}
