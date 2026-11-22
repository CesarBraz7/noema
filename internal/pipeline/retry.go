package pipeline

import (
	"context"
	"errors"

	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// RetryVideo requeues requests that were set aside because captionless videos had no transcriber.
func RetryVideo(ctx context.Context, database, rawURL string) (int, error) {
	canonical, err := video.CanonicalURL(rawURL)
	if err != nil {
		return 0, err
	}
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

		rows, err := tx.Query(ctx, `
			SELECT id FROM noema_ingestion_requests
			WHERE source_id = $1 AND state = 'NEEDS_REVIEW' AND starts_with(last_error, $2)
			ORDER BY id`, videoSourcePrefix+digest([]byte(canonical)), errReview.Error()+": "+missingCaptionsReview)
		if err != nil {
			return err
		}
		requests, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range requests {
			job := ingestionJob{RequestID: id, Transcribe: true}
			if _, err := client.InsertTx(ctx, tx, job, &river.InsertOpts{Queue: queueName, MaxAttempts: 3}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE noema_ingestion_requests
				SET state = 'PENDING', last_error = NULL, updated_at = now()
				WHERE id = $1`, id); err != nil {
				return err
			}
		}
		count = len(requests)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
