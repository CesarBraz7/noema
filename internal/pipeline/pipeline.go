// Package pipeline orchestrates the inbox and its River worker.
package pipeline

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/article"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/internal/inbox"
	"github.com/CesarBraz7/noema/internal/storage/github"
	"github.com/CesarBraz7/noema/schemas"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/001_article.sql
var migration string

const (
	inboxPath           = "Inbox/links.md"
	pollInterval        = 15 * time.Minute
	retryInterval       = 2 * time.Hour
	workerLock          = int64(0x4e6f656d61)
	defaultVideoAttempt = 15 * time.Minute
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

type App struct {
	database           string
	vault              *github.Repository
	artifacts          *github.Repository
	extractor          articleExtractor
	discoverVideo      func(context.Context, string) (video.Capture, error)
	readCaptions       func(context.Context, string) (video.Capture, error)
	transcribeVideo    func(context.Context, video.Capture) (video.Transcript, error)
	transcriberVersion string
	videoAttempt       time.Duration
	validator          schemas.Validator
	etag               string
}

func FromEnv(ctx context.Context, database string) (*App, error) {
	if database == "" {
		return nil, errors.New("defina DATABASE_URL_UNPOOLED ou DATABASE_URL com uma conexão direta")
	}
	owner := envDefault("GITHUB_OWNER", "CesarBraz7")
	vault := envDefault("GITHUB_VAULT_REPO", "obsidian-vault")
	artifacts := envDefault("GITHUB_ARTIFACTS_REPO", "noema-artifacts")
	if !repositoryName.MatchString(owner) || !repositoryName.MatchString(vault) || !repositoryName.MatchString(artifacts) || strings.EqualFold(vault, artifacts) {
		return nil, errors.New("configure nomes válidos e distintos para os repositórios de vault e artefatos")
	}
	vaultToken := envDefault("GITHUB_VAULT_TOKEN", os.Getenv("GITHUB_TOKEN"))
	artifactsToken := envDefault("GITHUB_ARTIFACTS_TOKEN", os.Getenv("GITHUB_TOKEN"))
	if vaultToken == "" || artifactsToken == "" {
		return nil, errors.New("defina os tokens GitHub do vault e dos artefatos")
	}
	extractor := article.New("trafilatura")
	if err := extractor.Check(ctx); err != nil {
		return nil, err
	}
	validator, err := schemas.New()
	if err != nil {
		return nil, err
	}
	window, err := envDuration("NOEMA_ASR_CHUNK_DURATION", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	attempt, err := envDuration("NOEMA_VIDEO_ATTEMPT_TIMEOUT", defaultVideoAttempt)
	if err != nil {
		return nil, err
	}
	app := &App{
		database: database, validator: validator, extractor: extractor, discoverVideo: video.Discover,
		readCaptions: video.Inspect, videoAttempt: attempt,
		vault:     github.New("https://api.github.com", owner, vault, vaultToken),
		artifacts: github.New("https://api.github.com", owner, artifacts, artifactsToken),
	}
	modelPath := envDefault("NOEMA_WHISPER_MODEL", video.WhisperModelPath)
	transcriber, err := video.NewTranscriber(envDefault("NOEMA_WORK_DIR", "data"), modelPath, window)
	if errors.Is(err, os.ErrNotExist) && os.Getenv("NOEMA_WHISPER_MODEL") == "" {
		return app, nil
	}
	if err != nil {
		return nil, fmt.Errorf("configurar ASR local: %w", err)
	}
	app.transcribeVideo, app.transcriberVersion = transcriber.Transcribe, transcriber.Version()
	return app, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(envDefault(name, fallback.String()))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s precisa ser uma duração positiva", name)
	}
	return value, nil
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func openDB(ctx context.Context, database string) (*pgxpool.Pool, error) {
	if database == "" {
		return nil, errors.New("defina DATABASE_URL_UNPOOLED ou DATABASE_URL com uma conexão direta")
	}
	config, err := pgxpool.ParseConfig(database)
	if err != nil {
		return nil, errors.New("DATABASE_URL inválida")
	}
	for _, endpoint := range append([]*pgconn.FallbackConfig{{Host: config.ConnConfig.Host, TLSConfig: config.ConnConfig.TLSConfig}}, config.ConnConfig.Fallbacks...) {
		switch endpoint.Host {
		case "localhost", "127.0.0.1", "::1":
		default:
			if endpoint.TLSConfig == nil {
				return nil, errors.New("use TLS na conexão PostgreSQL remota")
			}
		}
	}
	config.ConnConfig.ConnectTimeout = 10 * time.Second
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("falha na configuração do PostgreSQL")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("falha ao conectar ao PostgreSQL")
	}
	return pool, nil
}

func Migrate(ctx context.Context, database string) error {
	db, err := openDB(ctx, database)
	if err != nil {
		return err
	}
	defer db.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(db), nil)
	if err != nil {
		return err
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer closeSession(conn)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, workerLock); err != nil {
		return err
	}
	// River commits each migration separately so new enum values can be used by the next migration.
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, migration)
		return err
	})
}

// Cycle reloads the inbox and drains eligible jobs. Force also checks the database with an unchanged inbox.
func (a *App) Cycle(ctx context.Context, pollInbox, force bool) error {
	var etag string
	var entries []inbox.Entry
	var pollErr error
	if pollInbox {
		file, err := a.vault.Get(ctx, inboxPath, a.etag)
		switch {
		case err != nil:
			pollErr = err
		case file.NotModified:
		case !file.Exists:
			pollErr = errors.New("inbox ausente ou sem permissão de leitura no vault")
		default:
			etag = file.ETag
			entries = inbox.Parse(string(file.Data))
			if len(entries) == 0 {
				a.etag = etag
			}
		}
	}
	if !force && len(entries) == 0 {
		return pollErr
	}
	db, err := openDB(ctx, a.database)
	if err != nil {
		return errors.Join(pollErr, err)
	}
	defer db.Close()
	if len(entries) != 0 {
		if err := enqueue(ctx, db, entries); err != nil {
			return err
		}
		a.etag = etag
	}
	return errors.Join(pollErr, a.drain(ctx, db))
}

func (a *App) Run(ctx context.Context) {
	cycle := func(poll, force bool) {
		if err := a.Cycle(ctx, poll, force); err != nil && ctx.Err() == nil {
			slog.Error("ciclo de ingestão falhou", "error", err)
		}
	}
	cycle(true, true)
	poll := time.NewTicker(pollInterval)
	retry := time.NewTicker(retryInterval)
	defer poll.Stop()
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			cycle(true, false)
		case <-retry.C:
			cycle(false, true)
		}
	}
}

func enqueue(ctx context.Context, db *pgxpool.Pool, entries []inbox.Entry) error {
	client, err := river.NewClient(riverpgxv5.New(db), &river.Config{})
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		for _, entry := range entries {
			prefix := "web-"
			if entry.Video {
				prefix = videoSourcePrefix
			}
			id := prefix + digest([]byte(entry.URL))
			if _, err := tx.Exec(ctx, `INSERT INTO noema_sources (id, canonical_url) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, id, entry.URL); err != nil {
				return err
			}
			requestID, err := insertRequest(ctx, tx, id, entry.Focus)
			if err != nil {
				return err
			}
			if requestID != 0 {
				job := ingestionJob{RequestID: requestID, Transcribe: entry.Video}
				if _, err := client.InsertTx(ctx, tx, job, &river.InsertOpts{Queue: queueName, MaxAttempts: 3}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func insertRequest(ctx context.Context, tx pgx.Tx, sourceID, focus string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO noema_ingestion_requests (source_id, fingerprint, focus) VALUES ($1, $2, $3)
		ON CONFLICT (fingerprint) DO NOTHING RETURNING id`, sourceID, requestFingerprint(sourceID, focus), focus).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (a *App) drain(ctx context.Context, db *pgxpool.Pool) (err error) {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer closeSession(conn)
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, workerLock).Scan(&locked); err != nil || !locked {
		return err
	}
	eligible, err := eligibleJobs(ctx, db)
	if err != nil {
		return err
	}
	if eligible {
		if err := a.work(ctx, db); err != nil {
			return err
		}
	}
	// Update requests from any terminal River job whose worker stopped before saving its state.
	_, err = db.Exec(ctx, `
		UPDATE noema_ingestion_requests r
		SET state = 'FAILED', last_error = 'Job encerrado pelo River antes de confirmar o estado', updated_at = now()
		FROM river_job j
		WHERE j.kind = $1 AND j.queue = $2 AND j.state IN ('discarded', 'cancelled')
		AND r.id = (j.args->>'requestId')::bigint
		AND NOT EXISTS (
			SELECT 1 FROM river_job newer
			WHERE newer.kind = j.kind AND newer.queue = j.queue AND newer.id > j.id
			AND newer.args->>'requestId' = j.args->>'requestId'
		)
		AND r.state IN ('PENDING', 'CAPTURING', 'CONSOLIDATING', 'PUBLISHING', 'RETRY_SCHEDULED')`, (ingestionJob{}).Kind(), queueName)
	return err
}

// Closing the session releases its advisory lock even after a cancelled cycle.
func closeSession(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Hijack().Close(ctx)
}

func (a *App) work(ctx context.Context, db *pgxpool.Pool) (err error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &ingestionWorker{
		db: db, vault: a.vault, artifacts: a.artifacts, validator: a.validator,
		extractor: a.extractor, discoverVideo: a.discoverVideo, readCaptions: a.readCaptions,
		transcribeVideo: a.transcribeVideo, transcriberVersion: a.transcriberVersion, videoAttempt: a.videoAttempt,
	})
	client, err := river.NewClient(riverpgxv5.New(db), &river.Config{
		Queues: map[string]river.QueueConfig{queueName: {MaxWorkers: 1}}, Workers: workers,
		JobTimeout: 3 * time.Minute, RescueStuckJobsAfter: 4 * time.Minute,
	})
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, client.Stop(stopCtx))
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Stopped():
			return errors.New("worker River parou durante o ciclo")
		case <-ticker.C:
			eligible, err := eligibleJobs(ctx, db)
			if err != nil || !eligible {
				return err
			}
		}
	}
}

func eligibleJobs(ctx context.Context, db *pgxpool.Pool) (bool, error) {
	var eligible bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM river_job WHERE kind = $1 AND queue = $2
		AND (state IN ('available', 'running') OR (state IN ('retryable', 'scheduled') AND scheduled_at <= now())))`, (ingestionJob{}).Kind(), queueName).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("consultar fila River (execute noema migrate na instalação): %w", err)
	}
	return eligible, nil
}
