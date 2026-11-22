package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/article"
	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/internal/storage/github"
	"github.com/CesarBraz7/noema/schemas"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// Keep the persisted queue and job kind so existing article jobs can resume.
const queueName = "noema_articles"

var (
	errReview           = errors.New("revisão necessária")
	errArtifactTooLarge = errors.New("artefato excede o limite de tamanho")
)

const missingCaptionsReview = "vídeo sem legenda disponível; metadados salvos em "
const missingASRReview = "modelo de ASR local indisponível; metadados salvos em "

type ingestionJob struct {
	RequestID     int64  `json:"requestId"`
	Transcribe    bool   `json:"transcribe,omitempty"`
	VisualVersion string `json:"visualVersion,omitempty"`
}

func (ingestionJob) Kind() string { return "noema_article" }

type request struct {
	id            int64
	sourceID      string
	url           string
	focus         string
	visualVersion string
}

func (r request) isVideo() bool {
	return strings.HasPrefix(r.sourceID, videoSourcePrefix)
}

type articleExtractor interface {
	Extract(context.Context, string) (article.Document, error)
}

type ingestionWorker struct {
	river.WorkerDefaults[ingestionJob]
	db                 *pgxpool.Pool
	vault              *github.Repository
	artifacts          *github.Repository
	extractor          articleExtractor
	discoverVideo      func(context.Context, string) (video.Capture, error)
	readCaptions       func(context.Context, string) (video.Capture, error)
	transcribeVideo    func(context.Context, video.Capture) (video.Transcript, error)
	transcriberVersion string
	visual             *visualStage
	videoAttempt       time.Duration
	validator          schemas.Validator
}

func (*ingestionWorker) NextRetry(job *river.Job[ingestionJob]) time.Time {
	delay := time.Minute << min(max(job.Attempt-1, 0), 5)
	return time.Now().Add(delay + rand.N(delay/5))
}

func (w *ingestionWorker) Timeout(job *river.Job[ingestionJob]) time.Duration {
	if job.Args.Transcribe {
		if w.videoAttempt > 0 {
			return w.videoAttempt
		}
		return defaultVideoAttempt
	}
	return 0
}

func (w *ingestionWorker) Work(ctx context.Context, job *river.Job[ingestionJob]) error {
	req := request{id: job.Args.RequestID, visualVersion: job.Args.VisualVersion}
	var state string
	err := w.db.QueryRow(ctx, `
		SELECT r.source_id, s.canonical_url, r.focus, r.state
		FROM noema_ingestion_requests r JOIN noema_sources s ON s.id = r.source_id
		WHERE r.id = $1`, req.id).Scan(&req.sourceID, &req.url, &req.focus, &state)
	if err != nil {
		return err
	}
	switch state {
	case "COMPLETED":
		return nil
	case "ACCESS_REQUIRED", "NEEDS_REVIEW", "FAILED":
		return river.JobCancel(errors.New(state))
	}
	if _, err := w.db.Exec(ctx, `UPDATE noema_ingestion_requests SET attempt = $2 WHERE id = $1`, req.id, job.Attempt); err != nil {
		return err
	}

	if err := w.process(ctx, req); err != nil {
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if errors.Is(err, video.ErrMoreAudio) {
			if stateErr := w.state(statusCtx, req.id, "CAPTURING", err.Error()); stateErr != nil {
				return stateErr
			}
			return river.JobSnooze(0)
		}
		var quota *gemini.QuotaError
		if errors.As(err, &quota) {
			if stateErr := w.state(statusCtx, req.id, "WAITING_QUOTA", err.Error()); stateErr != nil {
				return stateErr
			}
			delay := quota.After
			if delay <= 0 {
				delay = retryInterval
			}
			return river.JobSnooze(delay)
		}
		state, permanent := "RETRY_SCHEDULED", false
		switch {
		case errors.Is(err, errReview):
			state, permanent = "NEEDS_REVIEW", true
		case errors.Is(err, article.ErrAccess), errors.Is(err, github.ErrPermission), errors.Is(err, video.ErrAudioAccess):
			state, permanent = "ACCESS_REQUIRED", true
		case errors.Is(err, article.ErrUnsupported), errors.Is(err, article.ErrUnsafe), errors.Is(err, errArtifactTooLarge):
			state, permanent = "FAILED", true
		case job.Attempt >= job.MaxAttempts:
			state = "FAILED"
		}
		if statusErr := w.state(statusCtx, req.id, state, err.Error()); statusErr != nil {
			return fmt.Errorf("registrar falha da fonte: %w", statusErr)
		}
		if permanent {
			return river.JobCancel(err)
		}
		return err
	}
	slog.Info("fonte processada", "request_id", req.id, "source_id", req.sourceID)
	return nil
}

func (w *ingestionWorker) process(ctx context.Context, req request) error {
	root := path.Join("sources", req.sourceID)
	if err := w.state(ctx, req.id, "CAPTURING", ""); err != nil {
		return err
	}
	var source sourceContent
	var captureRef artifact
	var err error
	extractorVersion := article.Version
	consolidationKind, noteFolder := consolidationVersion, "Artigos"
	if req.isVideo() {
		consolidationKind, noteFolder = videoConsolidationVersion, "Videos"
		source, captureRef, extractorVersion, err = w.captureVideo(ctx, req, root)
	} else {
		capturePath := path.Join(root, fingerprint(captureVersion, extractorVersion, req.url), "source-content.json")
		source, captureRef, err = w.capture(ctx, req, capturePath, "article", []string{}, func(ctx context.Context) (sourceContent, error) {
			doc, err := w.extractor.Extract(ctx, req.url)
			if err != nil {
				return sourceContent{}, err
			}
			return newSource(req.sourceID, req.url, doc, time.Now()), nil
		})
	}
	if err != nil {
		return err
	}
	var visual visualRecord
	var visualSkipped bool
	inputs := []artifact{captureRef}
	dependencies := []string{captureRef.SHA256}
	if req.isVideo() && req.visualVersion != "" {
		consolidationKind = videoVisualConsolidationVersion
		if source.Source.Availability != video.PublicAvailability {
			visualSkipped = true
		} else {
			record, ref, err := w.captureVisual(ctx, req, source, captureRef, root)
			if err != nil {
				return err
			}
			visual = record
			inputs = append(inputs, ref)
			dependencies = append(dependencies, ref.SHA256)
		}
	}
	if err := w.state(ctx, req.id, "CONSOLIDATING", ""); err != nil {
		return err
	}
	knowledge := consolidate(source, req.focus)
	if len(inputs) == 2 {
		knowledge.addVisual(visual, inputs[1])
	} else if visualSkipped {
		knowledge.Coverage.Unresolved = append(knowledge.Coverage.Unresolved, "A análise externa requer visibilidade pública confirmada pelo YouTube.")
	}
	keyParts := append([]string{consolidationKind}, dependencies...)
	key := fingerprint(append(keyParts, req.focus)...)
	knowledgePath := path.Join(root, key, "knowledge.json")
	knowledgeRef, err := w.putArtifact(ctx, req.sourceID, schemas.Knowledge, knowledgePath, knowledge, dependencies)
	if err != nil {
		return err
	}
	manifestPath := path.Join(root, key, "manifest.json")
	if _, err := w.putArtifact(ctx, req.sourceID, schemas.Manifest, manifestPath, manifest{
		SchemaVersion: schemaVersion, SourceID: req.sourceID, Fingerprint: key, PipelineVersion: consolidationKind,
		ExtractorVersion: extractorVersion, Artifacts: append(inputs, knowledgeRef),
	}, append(dependencies, knowledgeRef.SHA256)); err != nil {
		return err
	}
	if err := w.state(ctx, req.id, "PUBLISHING", ""); err != nil {
		return err
	}
	if err := w.publish(ctx, req, path.Join("Fontes", noteFolder, req.sourceID+".md"), note(source, knowledge, manifestPath)); err != nil {
		return err
	}
	return w.state(ctx, req.id, "COMPLETED", "")
}

func (w *ingestionWorker) captureVideo(ctx context.Context, req request, root string) (sourceContent, artifact, string, error) {
	metadataPath := path.Join(root, fingerprint(videoMetadataVersion, video.MetadataVersion, req.url), "source-content.json")
	metadata, ref, err := w.capture(ctx, req, metadataPath, "video", []string{}, func(ctx context.Context) (sourceContent, error) {
		capture, err := w.discoverVideo(ctx, req.url)
		if err != nil {
			return sourceContent{}, err
		}
		capture.URL = req.url
		return newVideoMetadata(req.sourceID, capture, time.Now()), nil
	})
	if err != nil {
		return sourceContent{}, artifact{}, "", err
	}
	if w.transcribeVideo == nil {
		return sourceContent{}, artifact{}, "", fmt.Errorf("%w: %s%s", errReview, missingASRReview, metadataPath)
	}
	dependencies := []string{ref.SHA256}
	location := path.Join(root, fingerprint(videoAudioVersion, w.transcriberVersion, ref.SHA256), "source-content.json")
	source, ref, err := w.capture(ctx, req, location, "video", dependencies, func(ctx context.Context) (sourceContent, error) {
		transcript, err := w.transcribeVideo(ctx, video.Capture{URL: req.url, Duration: metadata.Source.DurationSeconds})
		if err != nil {
			return sourceContent{}, err
		}
		return transcribedSource(metadata, transcript, time.Now()), nil
	})
	switch {
	case errors.Is(err, video.ErrAudioUnavailable):
		if w.readCaptions == nil {
			return sourceContent{}, artifact{}, "", fmt.Errorf("%w: %v; metadados salvos em %s", errReview, err, metadataPath)
		}
		location = path.Join(root, fingerprint(videoCaptionFallbackVersion, video.CaptionVersion, dependencies[0]), "source-content.json")
		source, ref, err := w.capture(ctx, req, location, "video", dependencies, func(ctx context.Context) (sourceContent, error) {
			captions, err := w.readCaptions(ctx, req.url)
			if err != nil {
				return sourceContent{}, err
			}
			if captions.CaptionsStatus == video.CaptionsFetchFailed {
				return sourceContent{}, errors.New("falha ao consultar legenda de fallback do YouTube")
			}
			if captions.Captions == nil || len(captions.Captions.Segments) == 0 {
				return sourceContent{}, fmt.Errorf("%w: áudio original e legenda de fallback indisponíveis; metadados salvos em %s", errReview, metadataPath)
			}
			return captionFallbackSource(metadata, *captions.Captions, time.Now()), nil
		})
		return source, ref, video.CaptionVersion, err
	case errors.Is(err, video.ErrNoSpeech):
		return sourceContent{}, artifact{}, "", fmt.Errorf("%w: %v; metadados salvos em %s", errReview, err, metadataPath)
	default:
		return source, ref, w.transcriberVersion, err
	}
}

func (w *ingestionWorker) capture(ctx context.Context, req request, location, sourceType string, dependencies []string, extract func(context.Context) (sourceContent, error)) (sourceContent, artifact, error) {
	var source sourceContent
	file, err := w.artifacts.Get(ctx, location, "")
	if err != nil {
		return source, artifact{}, err
	}
	if file.Exists {
		if w.validator.Validate(schemas.Source, file.Data) != nil || json.Unmarshal(file.Data, &source) != nil ||
			source.SourceID != req.sourceID || source.Source.URL != req.url || source.Source.Type != sourceType {
			return source, artifact{}, fmt.Errorf("%w: checkpoint de captura inválido", errReview)
		}
		if err := w.readTranscriptParts(ctx, &source, location); err != nil {
			return sourceContent{}, artifact{}, err
		}
		ref, err := w.recordArtifact(ctx, req.sourceID, schemas.Source, location, file.Data, transcriptDependencies(dependencies, source.TranscriptParts))
		return source, ref, err
	}
	var known bool
	if err := w.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM noema_artifacts WHERE path = $1)`, location).Scan(&known); err != nil {
		return source, artifact{}, err
	}
	if known {
		return source, artifact{}, fmt.Errorf("%w: checkpoint de captura ausente", errReview)
	}
	source, err = extract(ctx)
	if err != nil {
		return source, artifact{}, err
	}
	ref, err := w.putSource(ctx, source, location, dependencies)
	return source, ref, err
}

func (w *ingestionWorker) putArtifact(ctx context.Context, sourceID, kind, location string, value any, dependencies []string) (artifact, error) {
	data, err := encodeArtifact(value)
	if err != nil {
		return artifact{}, err
	}
	return w.putArtifactData(ctx, sourceID, kind, location, data, dependencies)
}

func encodeArtifact(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func (w *ingestionWorker) putArtifactData(ctx context.Context, sourceID, kind, location string, data []byte, dependencies []string) (artifact, error) {
	if len(data) > github.MaxFileSize {
		return artifact{}, errArtifactTooLarge
	}
	if err := w.validator.Validate(kind, data); err != nil {
		return artifact{}, fmt.Errorf("%w: %v", errReview, err)
	}
	file, err := w.artifacts.Get(ctx, location, "")
	if err != nil {
		return artifact{}, err
	}
	if !file.Exists {
		if err := w.artifacts.Put(ctx, location, "noema: publicar "+kind, data, ""); err != nil {
			return artifact{}, err
		}
		file, err = w.artifacts.Get(ctx, location, "")
		if err != nil {
			return artifact{}, err
		}
	}
	if !file.Exists {
		return artifact{}, errors.New("artefato ainda não disponível para verificação")
	}
	if !bytes.Equal(file.Data, data) {
		return artifact{}, fmt.Errorf("%w: conteúdo diverge do artefato imutável %s", errReview, kind)
	}
	return w.recordArtifact(ctx, sourceID, kind, location, data, dependencies)
}

func (w *ingestionWorker) recordArtifact(ctx context.Context, sourceID, kind, location string, data []byte, dependencies []string) (artifact, error) {
	ref := artifact{Kind: kind, Path: location, SHA256: digest(data), Size: len(data), Dependencies: dependencies, Validated: true}
	tag, err := w.db.Exec(ctx, `
		INSERT INTO noema_artifacts (path, source_id, kind, sha256, size) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (path) DO UPDATE SET verified_at = now()
		WHERE noema_artifacts.sha256 = excluded.sha256`, location, sourceID, kind, ref.SHA256, ref.Size)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: hash divergente para %s", errReview, kind)
	}
	return ref, err
}

func (w *ingestionWorker) state(ctx context.Context, id int64, state, message string) error {
	_, err := w.db.Exec(ctx, `
		UPDATE noema_ingestion_requests SET state = $2, last_error = NULLIF($3, ''), updated_at = now()
		WHERE id = $1`, id, state, message)
	return err
}

func (w *ingestionWorker) publish(ctx context.Context, req request, location string, data []byte) error {
	var lastRequest int64
	var lastPath, lastSHA string
	err := w.db.QueryRow(ctx, `SELECT request_id, note_path, blob_sha FROM noema_publications WHERE source_id = $1`, req.sourceID).Scan(&lastRequest, &lastPath, &lastSHA)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if lastRequest > req.id {
		return nil // A more recent focus already owns the note.
	}
	if req.isVideo() {
		legacyPath := path.Join("Fontes", "Vídeos", path.Base(location))
		useLegacy := lastPath == legacyPath
		if lastRequest == 0 {
			// An older worker might have committed the note before recording the publication.
			if err := w.db.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM noema_publication_intents WHERE note_path = $1 AND request_id <= $2
			)`, legacyPath, req.id).Scan(&useLegacy); err != nil {
				return err
			}
		}
		if useLegacy {
			location = legacyPath
		}
	}
	file, err := w.vault.Get(ctx, location, "")
	if err != nil {
		return err
	}
	if !file.Exists && lastRequest != 0 {
		return fmt.Errorf("%w: nota removida do vault", errReview)
	}
	if !file.Exists || !bytes.Equal(file.Data, data) {
		if file.Exists && file.SHA != lastSHA {
			// If a process stopped after the Git commit, an earlier durable intent can still identify its exact output.
			var intended bool
			err := w.db.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM noema_publication_intents WHERE note_path = $1 AND sha256 = $2 AND request_id >= $3 AND request_id <= $4)`,
				location, digest(file.Data), lastRequest, req.id).Scan(&intended)
			if err != nil {
				return err
			}
			if !intended {
				return fmt.Errorf("%w: nota existente tem conteúdo diferente do publicado pelo Noema", errReview)
			}
		}
		tag, err := w.db.Exec(ctx, `
			INSERT INTO noema_publication_intents (request_id, note_path, sha256) VALUES ($1, $2, $3)
			ON CONFLICT (request_id) DO UPDATE SET sha256 = excluded.sha256
			WHERE noema_publication_intents.sha256 = excluded.sha256`, req.id, location, digest(data))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: publicação pendente mudou desde a última tentativa", errReview)
		}
		message := "noema: publicar nota de artigo"
		if req.isVideo() {
			message = "noema: publicar nota de vídeo"
		}
		if err := w.vault.Put(ctx, location, message, data, file.SHA); err != nil {
			return err
		}
		file, err = w.vault.Get(ctx, location, "")
		if err != nil {
			return err
		}
		if !file.Exists || !bytes.Equal(file.Data, data) {
			return fmt.Errorf("%w: o vault mudou durante a publicação", errReview)
		}
	}
	_, err = w.db.Exec(ctx, `
		INSERT INTO noema_publications (source_id, request_id, note_path, blob_sha) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source_id) DO UPDATE SET request_id = excluded.request_id, blob_sha = excluded.blob_sha, published_at = now()
		WHERE noema_publications.request_id <= excluded.request_id`, req.sourceID, req.id, location, file.SHA)
	return err
}
