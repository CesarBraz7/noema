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

type ingestionJob struct {
	RequestID int64 `json:"requestId"`
}

func (ingestionJob) Kind() string { return "noema_article" }

type request struct {
	id       int64
	sourceID string
	url      string
	focus    string
}

func (r request) isVideo() bool {
	return strings.HasPrefix(r.sourceID, videoSourcePrefix)
}

type articleExtractor interface {
	Extract(context.Context, string) (article.Document, error)
}

type ingestionWorker struct {
	river.WorkerDefaults[ingestionJob]
	db           *pgxpool.Pool
	vault        *github.Repository
	artifacts    *github.Repository
	extractor    articleExtractor
	inspectVideo func(context.Context, string) (video.Capture, error)
	validator    schemas.Validator
}

func (*ingestionWorker) NextRetry(job *river.Job[ingestionJob]) time.Time {
	delay := time.Minute << min(max(job.Attempt-1, 0), 5)
	return time.Now().Add(delay + rand.N(delay/5))
}

func (w *ingestionWorker) Work(ctx context.Context, job *river.Job[ingestionJob]) error {
	req := request{id: job.Args.RequestID}
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
		state, permanent := "RETRY_SCHEDULED", false
		switch {
		case errors.Is(err, errReview):
			state, permanent = "NEEDS_REVIEW", true
		case errors.Is(err, article.ErrAccess), errors.Is(err, github.ErrPermission):
			state, permanent = "ACCESS_REQUIRED", true
		case errors.Is(err, article.ErrUnsupported), errors.Is(err, article.ErrUnsafe), errors.Is(err, errArtifactTooLarge):
			state, permanent = "FAILED", true
		case job.Attempt >= job.MaxAttempts:
			state = "FAILED"
		}
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
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
	captureKind, extractorVersion := captureVersion, article.Version
	consolidationKind, noteFolder, sourceType := consolidationVersion, "Artigos", "article"
	if req.isVideo() {
		captureKind, extractorVersion = videoCaptureVersion, video.Version
		consolidationKind, noteFolder, sourceType = videoConsolidationVersion, "Vídeos", "video"
	}
	capturePath := path.Join(root, fingerprint(captureKind, extractorVersion, req.url), "source-content.json")
	if err := w.state(ctx, req.id, "CAPTURING", ""); err != nil {
		return err
	}
	source, captureRef, err := w.capture(ctx, req, capturePath, sourceType)
	if err != nil {
		return err
	}
	if len(source.Blocks) == 0 {
		return fmt.Errorf("%w: vídeo sem legenda disponível; metadados salvos em %s", errReview, capturePath)
	}
	if err := w.state(ctx, req.id, "CONSOLIDATING", ""); err != nil {
		return err
	}
	knowledge := consolidate(source, req.focus)
	key := fingerprint(consolidationKind, captureRef.SHA256, req.focus)
	knowledgePath := path.Join(root, key, "knowledge.json")
	knowledgeRef, err := w.putArtifact(ctx, req.sourceID, schemas.Knowledge, knowledgePath, knowledge, []string{captureRef.SHA256})
	if err != nil {
		return err
	}
	manifestPath := path.Join(root, key, "manifest.json")
	if _, err := w.putArtifact(ctx, req.sourceID, schemas.Manifest, manifestPath, manifest{
		SchemaVersion: schemaVersion, SourceID: req.sourceID, Fingerprint: key, PipelineVersion: consolidationKind,
		ExtractorVersion: extractorVersion, Artifacts: []artifact{captureRef, knowledgeRef},
	}, []string{captureRef.SHA256, knowledgeRef.SHA256}); err != nil {
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

func (w *ingestionWorker) capture(ctx context.Context, req request, location, sourceType string) (sourceContent, artifact, error) {
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
		ref, err := w.recordArtifact(ctx, req.sourceID, schemas.Source, location, file.Data, []string{})
		return source, ref, err
	}
	var known bool
	if err := w.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM noema_artifacts WHERE path = $1)`, location).Scan(&known); err != nil {
		return source, artifact{}, err
	}
	if known {
		return source, artifact{}, fmt.Errorf("%w: checkpoint de captura ausente", errReview)
	}
	source, err = w.extract(ctx, req)
	if err != nil {
		return source, artifact{}, err
	}
	ref, err := w.putArtifact(ctx, req.sourceID, schemas.Source, location, source, []string{})
	return source, ref, err
}

func (w *ingestionWorker) extract(ctx context.Context, req request) (sourceContent, error) {
	if req.isVideo() {
		videoCapture, err := w.inspectVideo(ctx, req.url)
		if err != nil {
			return sourceContent{}, err
		}
		if videoCapture.CaptionsStatus == video.CaptionsFetchFailed {
			return sourceContent{}, errors.New("falha ao buscar a legenda do YouTube")
		}
		return newVideoSource(req.sourceID, videoCapture, time.Now()), nil
	}
	doc, err := w.extractor.Extract(ctx, req.url)
	if err != nil {
		return sourceContent{}, err
	}
	return newSource(req.sourceID, req.url, doc, time.Now()), nil
}

func (w *ingestionWorker) putArtifact(ctx context.Context, sourceID, kind, location string, value any, dependencies []string) (artifact, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return artifact{}, err
	}
	data = append(data, '\n')
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
	var lastSHA string
	err := w.db.QueryRow(ctx, `SELECT request_id, blob_sha FROM noema_publications WHERE source_id = $1`, req.sourceID).Scan(&lastRequest, &lastSHA)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if lastRequest > req.id {
		return nil // A more recent focus already owns the note.
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
