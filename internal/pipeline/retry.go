package pipeline

import (
	"context"
	"errors"

	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// RetryVideo resumes requests in review after a video capture issue.
func RetryVideo(ctx context.Context, database, rawURL string) (int, error) {
	canonical, err := video.CanonicalURL(rawURL)
	if err != nil {
		return 0, err
	}
	visual, err := visualFromEnv()
	if err != nil {
		return 0, err
	}
	visualVersion := visual.requestVersion()
	db, err := openDB(ctx, database)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	client, err := river.NewClient(riverpgxv5.New(db), &river.Config{})
	if err != nil {
		return 0, err
	}

	count := 0
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, workerLock).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return errors.New("o worker está processando a fila; tente reenfileirar após o ciclo")
		}

		sourceID := videoSourcePrefix + digest([]byte(canonical))
		reasons := []string{
			errReview.Error() + ": " + missingCaptionsReview,
			errReview.Error() + ": " + missingASRReview,
			errReview.Error() + ": " + video.ErrAudioUnavailable.Error() + ": a duração precisa estar entre ",
			errReview.Error() + ": " + video.ErrNoSpeech.Error(),
			errReview.Error() + ": áudio original e legenda de fallback indisponíveis",
			errReview.Error() + ": " + missingVisualConfigReview,
			errReview.Error() + ": " + gemini.ErrAuthorization.Error(),
			errReview.Error() + ": " + gemini.ErrRejected.Error(),
		}
		type review struct {
			ID          int64
			Fingerprint string
			Focus       string
		}
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT ON (focus) id, fingerprint, focus FROM noema_ingestion_requests
			WHERE source_id = $1 AND state = 'NEEDS_REVIEW'
			AND EXISTS (SELECT 1 FROM unnest($2::text[]) AS reason WHERE starts_with(last_error, reason))
			ORDER BY focus, id DESC`, sourceID, reasons)
		if err != nil {
			return err
		}
		requests, err := pgx.CollectRows(rows, pgx.RowToStructByPos[review])
		if err != nil {
			return err
		}
		for _, request := range requests {
			id := request.ID
			if request.Fingerprint != requestFingerprint(sourceID, request.Focus, visualVersion) {
				id, err = insertRequest(ctx, tx, sourceID, request.Focus, visualVersion)
				if err != nil {
					return err
				}
				if id == 0 {
					continue
				}
			} else if _, err := tx.Exec(ctx, `
				UPDATE noema_ingestion_requests
				SET state = 'PENDING', last_error = NULL, updated_at = now()
				WHERE id = $1`, id); err != nil {
				return err
			}
			job := ingestionJob{RequestID: id, Transcribe: true, VisualVersion: visualVersion}
			if _, err := client.InsertTx(ctx, tx, job, &river.InsertOpts{Queue: queueName, MaxAttempts: 3}); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
