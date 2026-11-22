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

type visualStage struct {
	model   string
	workDir string
	client  visualAnalyzer
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
	return &visualStage{model: model, workDir: envDefault("NOEMA_WORK_DIR", "data"), client: client}, nil
}

func (v *visualStage) version() string {
	if v == nil {
		return ""
	}
	return fingerprint("gemini", gemini.PromptVersion, visualURLMode, v.model)
}

type visualRecord struct {
	SchemaVersion  string        `json:"schemaVersion"`
	SourceID       string        `json:"sourceId"`
	URL            string        `json:"sourceUrl"`
	Provider       string        `json:"provider"`
	Mode           string        `json:"analysisMode"`
	Prompt         string        `json:"promptVersion"`
	RequestedModel string        `json:"requestedModel"`
	MetadataSHA    string        `json:"metadataSha256"`
	DurationMS     int64         `json:"durationMs"`
	CapturedAt     time.Time     `json:"analyzedAt"`
	BudgetUSD      int           `json:"budgetUsd"`
	Response       gemini.Result `json:"response"`
}

func (w *ingestionWorker) captureVisual(ctx context.Context, req request, source sourceContent, captureRef artifact, root string) (visualRecord, artifact, error) {
	if w.visual.version() != req.visualVersion {
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
	key := fingerprint(req.visualVersion, metadataSHA, strconv.FormatInt(durationMS, 10))
	location := path.Join(root, key, "video-visual.json")
	template := visualRecord{
		SchemaVersion: schemaVersion, SourceID: req.sourceID, URL: req.url, Provider: "gemini", Mode: visualURLMode,
		Prompt: gemini.PromptVersion, RequestedModel: w.visual.model, MetadataSHA: metadataSHA, DurationMS: durationMS,
	}
	file, err := w.artifacts.Get(ctx, location, "")
	if err != nil {
		return visualRecord{}, artifact{}, err
	}
	if file.Exists {
		record, err := w.readVisualRecord(file.Data, template)
		if err != nil {
			return visualRecord{}, artifact{}, err
		}
		ref, err := w.recordArtifact(ctx, req.sourceID, schemas.Visual, location, file.Data, []string{metadataSHA})
		return record, ref, err
	}
	var known bool
	if err := w.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM noema_artifacts WHERE path = $1)`, location).Scan(&known); err != nil {
		return visualRecord{}, artifact{}, err
	}
	if known {
		return visualRecord{}, artifact{}, fmt.Errorf("%w: checkpoint de análise visual ausente", errReview)
	}

	cache := filepath.Join(w.visual.workDir, "video-visual", key+".json")
	record, err := w.localVisualRecord(ctx, cache, template)
	if err != nil {
		return visualRecord{}, artifact{}, err
	}
	ref, err := w.putArtifact(ctx, req.sourceID, schemas.Visual, location, record, []string{metadataSHA})
	return record, ref, err
}

func (w *ingestionWorker) localVisualRecord(ctx context.Context, cache string, template visualRecord) (visualRecord, error) {
	if data, err := os.ReadFile(cache); err == nil {
		return w.readVisualRecord(data, template)
	} else if !errors.Is(err, os.ErrNotExist) {
		return visualRecord{}, err
	}
	response, err := w.visual.client.AnalyzeVideo(ctx, template.URL, template.DurationMS)
	if errors.Is(err, gemini.ErrAuthorization) || errors.Is(err, gemini.ErrRejected) {
		return visualRecord{}, fmt.Errorf("%w: %w", errReview, err)
	}
	if err != nil {
		return visualRecord{}, err
	}
	for i := range response.Observations {
		response.Observations[i].ID = fmt.Sprintf("v-%03d", i+1)
	}
	template.CapturedAt, template.Response = time.Now().UTC().Truncate(time.Second), response
	data, err := encodeArtifact(template)
	if err != nil {
		return visualRecord{}, err
	}
	if _, err := w.readVisualRecord(data, template); err != nil {
		return visualRecord{}, err
	}
	if err := local.PutJSON(cache, template); err != nil {
		return visualRecord{}, err
	}
	return template, nil
}

func (w *ingestionWorker) readVisualRecord(data []byte, expected visualRecord) (visualRecord, error) {
	var actual visualRecord
	if w.validator.Validate(schemas.Visual, data) != nil || json.Unmarshal(data, &actual) != nil ||
		actual.SourceID != expected.SourceID || actual.URL != expected.URL || actual.MetadataSHA != expected.MetadataSHA ||
		actual.Mode != expected.Mode || actual.Prompt != expected.Prompt || actual.RequestedModel != expected.RequestedModel ||
		actual.DurationMS != expected.DurationMS || !actual.Response.Valid(actual.DurationMS) {
		return visualRecord{}, fmt.Errorf("%w: checkpoint de análise visual inválido", errReview)
	}
	for i, observation := range actual.Response.Observations {
		if observation.ID != fmt.Sprintf("v-%03d", i+1) {
			return visualRecord{}, fmt.Errorf("%w: evidência visual sem identificador estável", errReview)
		}
	}
	return actual, nil
}
