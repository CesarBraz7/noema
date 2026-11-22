package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/article"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/internal/storage/github"
	"github.com/CesarBraz7/noema/schemas"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArticleFlow(t *testing.T) {
	dsn := os.Getenv("NOEMA_INTEGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("defina NOEMA_INTEGRATION_DATABASE_URL com um banco de teste vazio")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := openDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var clean bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('noema_sources') IS NULL AND to_regclass('river_job') IS NULL`).Scan(&clean); err != nil || !clean {
		t.Fatalf("banco de integração deve estar vazio: %v", err)
	}
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("migração deve ser idempotente: %v", err)
	}
	validator, err := schemas.New()
	if err != nil {
		t.Fatal(err)
	}
	server := &testGitHub{files: map[string]string{}, loseCaptureResponse: true}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var captures atomic.Int32
	app := &App{
		database: dsn, validator: validator,
		vault:     github.New(httpServer.URL, "owner", "vault", "fake-token"),
		artifacts: github.New(httpServer.URL, "owner", "artifacts", "fake-token"),
		extractor: extractorFunc(func(_ context.Context, url string) (article.Document, error) {
			captures.Add(1)
			return article.Document{
				URL: url, Title: "Artigo de teste", Text: "Texto inicial.\nOutro trecho.\nMais informação.\nImportância dos checkpoints.",
			}, nil
		}),
	}
	const inbox = "/repos/owner/vault/contents/Inbox/links.md"
	id := "web-" + digest([]byte("https://example.com/artigo"))
	note := "/repos/owner/vault/contents/Fontes/Artigos/" + id + ".md"
	server.set(inbox, "- [ ] https://EXAMPLE.com/artigo?utm_source=mail#trecho\n- [x] https://example.com/artigo\n")
	cycle := func(force bool) {
		t.Helper()
		if err := app.Cycle(ctx, true, force); err != nil {
			t.Fatal(err)
		}
	}
	cycle(true)
	assertStates(t, db, "RETRY_SCHEDULED")
	assertRowCount(t, db, "noema_artifacts", 0)
	makeRetryDue(t, ctx, db)
	cycle(true) // Recover the uploaded capture before extracting the article again.
	assertRowCount(t, db, "noema_sources", 1)
	assertRowCount(t, db, "noema_ingestion_requests", 1)
	assertRowCount(t, db, "noema_artifacts", 3)
	assertStates(t, db, "COMPLETED")
	if got := server.get(note); !strings.Contains(got, "Trechos do artigo") || !strings.Contains(got, "`b-001`") {
		t.Fatalf("nota sem evidência: %q", got)
	}
	root := path.Join("sources", id)
	capturePath := path.Join(root, fingerprint(captureVersion, article.Version, "https://example.com/artigo"), "source-content.json")
	const artifactRepo = "/repos/owner/artifacts/contents/"
	captureData := server.get(artifactRepo + capturePath)
	captureHash := digest([]byte(captureData))
	manifestPath := path.Join(root, fingerprint(consolidationVersion, captureHash, ""), "manifest.json")
	var saved manifest
	if err := json.Unmarshal([]byte(server.get(artifactRepo+manifestPath)), &saved); err != nil || len(saved.Artifacts) != 2 {
		t.Fatalf("manifesto inválido: %v", err)
	}
	for _, ref := range saved.Artifacts {
		data := []byte(server.get(artifactRepo + ref.Path))
		if len(data) == 0 || ref.Size != len(data) || ref.SHA256 != digest(data) {
			t.Fatalf("referência de manifesto divergente: %#v", ref)
		}
	}
	app.database = ""
	cycle(false) // The 304 must avoid opening the database.
	app.database = dsn

	server.set(inbox, "- https://example.com/artigo\n  - Foco: checkpoints\n")
	cycle(false)
	assertStates(t, db, "COMPLETED,COMPLETED")
	assertRowCount(t, db, "noema_artifacts", 5)
	if got := server.get(note); !strings.Contains(got, "Foco: checkpoints") || !strings.Contains(got, "`b-004`") {
		t.Fatalf("nota não refletiu foco: %q", got)
	}
	if got := captures.Load(); got != 1 {
		t.Fatalf("capturas após reenvios = %d, esperado 1", got)
	}

	server.editBeforeNextNoteWrite(note, "# Edição humana durante a publicação\n")
	server.set(inbox, "- https://example.com/artigo\n  - Outra observação do usuário\n")
	cycle(false)
	assertStates(t, db, "COMPLETED,COMPLETED,RETRY_SCHEDULED")
	makeRetryDue(t, ctx, db)
	app.etag = ""
	cycle(true)
	assertStates(t, db, "COMPLETED,COMPLETED,NEEDS_REVIEW")
	assertRowCount(t, db, "noema_artifacts", 7)
	if got := server.get(note); got != "# Edição humana durante a publicação\n" {
		t.Fatalf("edição humana alterada: %q", got)
	}
	if got := captures.Load(); got != 1 {
		t.Fatalf("capturas após retry e conflito = %d, esperado 1", got)
	}
	t.Run("YouTube", func(t *testing.T) {
		const (
			canonical    = "https://www.youtube.com/watch?v=abcdefghijk"
			noCaptions   = "https://www.youtube.com/watch?v=12345678901"
			repoLocation = "/repos/owner/artifacts/contents/"
		)
		videoID := videoSourcePrefix + digest([]byte(canonical))
		partialID := videoSourcePrefix + digest([]byte(noCaptions))
		location := func(id, url string) string {
			return path.Join("sources", id, fingerprint(videoCaptureVersion, video.Version, url), "source-content.json")
		}
		var videoCaptures atomic.Int32
		app.inspectVideo = func(_ context.Context, url string) (video.Capture, error) {
			videoCaptures.Add(1)
			doc := video.Capture{URL: url, Title: "Vídeo de teste", Channel: "Canal de teste", Duration: 132}
			if url == noCaptions {
				doc.CaptionsStatus = video.CaptionsUnavailable
				return doc, nil
			}
			doc.CaptionsStatus = video.CaptionsAvailable
			doc.Captions = &video.CaptionTrack{
				Language: "pt-BR", Segments: []video.Segment{
					{StartMS: 0, DurationMS: 1250, Text: "Abertura."},
					{StartMS: 62120, DurationMS: 3000, Text: "Checkpoint de vídeo."},
				},
			}
			return doc, nil
		}
		server.set(inbox, "- https://youtu.be/abcdefghijk?si=share\n- "+canonical+"&t=10s&list=ignored\n- "+noCaptions+"\n")
		if err := app.Cycle(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		assertRowCount(t, db, "noema_sources", 3)
		assertStates(t, db, "COMPLETED,COMPLETED,NEEDS_REVIEW,COMPLETED,NEEDS_REVIEW")
		if got := videoCaptures.Load(); got != 2 {
			t.Fatalf("esperava uma captura por vídeo, obtive %d", got)
		}
		checkpoint := server.get(repoLocation + location(videoID, canonical))
		var saved sourceContent
		if err := json.Unmarshal([]byte(checkpoint), &saved); err != nil || saved.Source.URL != canonical ||
			saved.Source.Type != "video" || saved.Source.Platform != "youtube" || saved.Capture.CaptionLanguage != "pt-BR" ||
			saved.Capture.AutomaticCaptions == nil || *saved.Capture.AutomaticCaptions || len(saved.Blocks) != 2 {
			t.Fatalf("captura de vídeo inválida: %s (%v)", checkpoint, err)
		}
		first := saved.Blocks[0]
		if first.Kind != "transcript_segment" || first.StartMS == nil || *first.StartMS != 0 ||
			first.EndMS == nil || *first.EndMS != 1250 {
			t.Fatalf("timestamps do primeiro bloco: %+v", first)
		}
		manifestPath := path.Join("sources", videoID, fingerprint(videoConsolidationVersion, digest([]byte(checkpoint)), ""), "manifest.json")
		var savedManifest manifest
		if err := json.Unmarshal([]byte(server.get(repoLocation+manifestPath)), &savedManifest); err != nil ||
			len(savedManifest.Artifacts) != 2 || savedManifest.Artifacts[0].SHA256 != digest([]byte(checkpoint)) {
			t.Fatalf("manifesto de vídeo não referencia a captura: %+v (%v)", savedManifest, err)
		}
		videoNote := "/repos/owner/vault/contents/Fontes/Vídeos/" + videoID + ".md"
		if got := server.get(videoNote); !strings.Contains(got, "Trechos da legenda") ||
			!strings.Contains(got, "00:01:02.120–00:01:05.120") || !strings.Contains(got, "`b-002`") {
			t.Fatalf("nota do vídeo sem evidência temporal: %q", got)
		}
		partial := server.get(repoLocation + location(partialID, noCaptions))
		if !strings.Contains(partial, "Nenhuma legenda compatível disponível.") ||
			server.get("/repos/owner/vault/contents/Fontes/Vídeos/"+partialID+".md") != "" {
			t.Fatalf("esperava apenas a captura parcial para revisão: %s", partial)
		}
		server.set(inbox, "- "+canonical+"\n  - Foco: checkpoint\n")
		if err := app.Cycle(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		assertStates(t, db, "COMPLETED,COMPLETED,NEEDS_REVIEW,COMPLETED,NEEDS_REVIEW,COMPLETED")
		if got := videoCaptures.Load(); got != 2 {
			t.Fatalf("o novo foco deveria reutilizar a legenda, capturas = %d", got)
		}
		if got := server.get(videoNote); !strings.Contains(got, "Foco: checkpoint") {
			t.Fatalf("nota do vídeo sem o novo foco: %q", got)
		}

		var transcriptions atomic.Int32
		app.transcribeVideo = func(_ context.Context, doc video.Capture) (video.Transcript, error) {
			transcriptions.Add(1)
			if doc.URL != noCaptions || doc.Duration != 132 {
				t.Errorf("metadados reutilizados na transcrição: %+v", doc)
			}
			return video.Transcript{Language: "pt", Segments: []video.Segment{
				{StartMS: 330, DurationMS: 1400, Text: "Trecho transcrito."},
			}}, nil
		}
		for _, retry := range []struct {
			url   string
			count int
		}{
			{canonical, 0},
			{"https://youtu.be/12345678901?si=retry", 1},
			{noCaptions, 0},
		} {
			count, err := RetryVideo(ctx, dsn, retry.url)
			if err != nil || count != retry.count {
				t.Fatalf("RetryVideo(%q) = %d, %v, esperava %d", retry.url, count, err, retry.count)
			}
		}
		if err := app.Cycle(ctx, false, true); err != nil {
			t.Fatal(err)
		}
		assertStates(t, db, "COMPLETED,COMPLETED,NEEDS_REVIEW,COMPLETED,COMPLETED,COMPLETED")
		retryNote := "/repos/owner/vault/contents/Fontes/Vídeos/" + partialID + ".md"
		if got := server.get(retryNote); !strings.Contains(got, "Trechos da transcrição") ||
			!strings.Contains(got, "00:00:00.330–00:00:01.730") {
			t.Fatalf("nota sem transcrição temporal: %q", got)
		}
		if capture := server.get(repoLocation + location(partialID, noCaptions)); capture != partial {
			t.Fatal("a retomada alterou o checkpoint original")
		}
		server.set(inbox, "- "+noCaptions+"\n  - Foco: trecho\n")
		if err := app.Cycle(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		if videoCaptures.Load() != 2 || transcriptions.Load() != 1 {
			t.Fatalf("a retomada ou o novo foco repetiu extração: capturas %d, transcrições %d", videoCaptures.Load(), transcriptions.Load())
		}
	})
}

// Move one retry forward instead of waiting for its backoff during the test.
func makeRetryDue(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	tag, err := db.Exec(ctx, `UPDATE river_job SET scheduled_at = now() - interval '1 second' WHERE state IN ('retryable', 'available')`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("esperado um retry no River: %v", err)
	}
}

func assertStates(t *testing.T, db *pgxpool.Pool, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(context.Background(), `SELECT string_agg(state, ',' ORDER BY id) FROM noema_ingestion_requests`).Scan(&got); err != nil || got != want {
		t.Fatalf("estados = %q, esperado %q, erro %v", got, want, err)
	}
}

func assertRowCount(t *testing.T, db *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&got); err != nil || got != want {
		t.Fatalf("linhas em %s = %d, esperado %d, erro %v", table, got, want, err)
	}
}

type extractorFunc func(context.Context, string) (article.Document, error)

func (f extractorFunc) Extract(ctx context.Context, url string) (article.Document, error) {
	return f(ctx, url)
}

type testGitHub struct {
	mu                  sync.Mutex
	files               map[string]string
	concurrent          string
	changedNote         string
	loseCaptureResponse bool
}

func (s *testGitHub) set(path, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = data
}

func (s *testGitHub) get(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files[path]
}

func (s *testGitHub) editBeforeNextNoteWrite(path, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.concurrent, s.changedNote = path, data
}

func (s *testGitHub) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Header.Get("Authorization") != "Bearer fake-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := req.URL.Path
	if req.Method == http.MethodPut && path == s.concurrent {
		s.files[path] = s.changedNote
		s.concurrent = ""
	}
	current, exists := s.files[path]
	var sha string
	if exists {
		sha = digest([]byte(current))
	}
	switch req.Method {
	case http.MethodGet:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		etag := `"` + sha + `"`
		w.Header().Set("ETag", etag)
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": sha, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(current))})
	case http.MethodPut:
		var payload struct{ SHA, Content string }
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || payload.SHA != sha {
			w.WriteHeader(http.StatusConflict)
			return
		}
		data, err := base64.StdEncoding.DecodeString(payload.Content)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.files[path] = string(data)
		if s.loseCaptureResponse && strings.HasSuffix(path, "/source-content.json") {
			s.loseCaptureResponse = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
