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
			fallback     = "https://www.youtube.com/watch?v=12345678901"
			restricted   = "https://www.youtube.com/watch?v=ZYXWVUTSRQP"
			legacyReview = "https://www.youtube.com/watch?v=09876543210"
			repoLocation = "/repos/owner/artifacts/contents/"
			vaultContent = "/repos/owner/vault/contents/"
		)
		sourceID := func(url string) string { return videoSourcePrefix + digest([]byte(url)) }
		metadataPath := func(url string) string {
			return path.Join("sources", sourceID(url), fingerprint(videoMetadataVersion, video.MetadataVersion, url), "source-content.json")
		}
		notePath := func(url string) string {
			return vaultContent + "Fontes/Videos/" + sourceID(url) + ".md"
		}
		seedLegacy := func(url, state, reason string) {
			t.Helper()
			if _, err := db.Exec(ctx, "INSERT INTO noema_sources (id, canonical_url) VALUES ($1, $2)", sourceID(url), url); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, "INSERT INTO noema_ingestion_requests (source_id, fingerprint, focus, state, last_error) VALUES ($1, $2, '', $3, $4)",
				sourceID(url), fingerprint(sourceID(url), ""), state, reason); err != nil {
				t.Fatal(err)
			}
		}
		assertVideoState := func(url, focus, want string) {
			t.Helper()
			var got string
			err := db.QueryRow(ctx, "SELECT state FROM noema_ingestion_requests WHERE fingerprint = $1",
				requestFingerprint(sourceID(url), focus)).Scan(&got)
			if err != nil || got != want {
				t.Fatalf("estado do vídeo %q com foco %q = %q, esperado %q (%v)", url, focus, got, want, err)
			}
		}
		cycleVideos := func(poll bool) {
			t.Helper()
			if err := app.Cycle(ctx, poll, true); err != nil {
				t.Fatal(err)
			}
		}

		seedLegacy(canonical, "COMPLETED", "")
		oldPath := path.Join("sources", sourceID(canonical), fingerprint("youtube-capture-v1", video.CaptionVersion, canonical), "source-content.json")
		oldMetadata := newVideoMetadata(sourceID(canonical), video.Capture{URL: canonical, Title: "Captura anterior"}, time.Now())
		oldCapture := captionFallbackSource(oldMetadata, video.CaptionTrack{Language: "pt", Segments: []video.Segment{
			{StartMS: 0, DurationMS: 1000, Text: "Legenda histórica."},
		}}, time.Now())
		oldCapture.Capture.CaptionsFallback = false
		oldBytes, err := encodeArtifact(oldCapture)
		if err != nil {
			t.Fatal(err)
		}
		server.set(repoLocation+oldPath, string(oldBytes))
		seedLegacy(fallback, "COMPLETED", "")
		legacyNote := "Fontes/Vídeos/" + sourceID(fallback) + ".md"
		legacyData := "# Nota publicada antes do ajuste da pasta\n"
		server.set(vaultContent+legacyNote, legacyData)
		if _, err := db.Exec(ctx, `
			INSERT INTO noema_publications (source_id, request_id, note_path, blob_sha)
			SELECT source_id, id, $2, $3 FROM noema_ingestion_requests WHERE source_id = $1`,
			sourceID(fallback), legacyNote, digest([]byte(legacyData))); err != nil {
			t.Fatal(err)
		}

		var metadataReads, captionsRead, audioRuns, longAudioRuns atomic.Int32
		app.discoverVideo = func(_ context.Context, url string) (video.Capture, error) {
			metadataReads.Add(1)
			return video.Capture{URL: url, Title: "Vídeo de teste", Duration: 1300,
				Chapters: []video.Chapter{
					{Title: "Introdução", StartMS: 0, EndMS: 1221200},
					{Title: "Checkpoints", StartMS: 1221200, EndMS: 1300000},
				},
				CaptionsStatus: video.CaptionsAvailable,
				Captions:       &video.CaptionTrack{Language: "pt", Segments: []video.Segment{{Text: "Legenda da plataforma."}}},
			}, nil
		}
		app.readCaptions = func(_ context.Context, url string) (video.Capture, error) {
			captionsRead.Add(1)
			if url != fallback {
				t.Errorf("consultou legendas sem falha definitiva na obtenção do áudio: %s", url)
			}
			return video.Capture{URL: url, CaptionsStatus: video.CaptionsAvailable, Captions: &video.CaptionTrack{
				Language: "pt", Segments: []video.Segment{{StartMS: 0, DurationMS: 2500, Text: "Legenda de fallback."}},
			}}, nil
		}
		app.transcriberVersion = "whisper.cpp-test"
		app.transcribeVideo = func(_ context.Context, doc video.Capture) (video.Transcript, error) {
			audioRuns.Add(1)
			switch doc.URL {
			case fallback:
				return video.Transcript{}, video.ErrAudioUnavailable
			case restricted:
				return video.Transcript{}, video.ErrAudioAccess
			case canonical:
				if longAudioRuns.Add(1) == 1 {
					return video.Transcript{}, video.ErrMoreAudio
				}
			}
			if doc.Duration <= 1200 {
				t.Errorf("o teste precisa exercitar fonte acima de 20 minutos: %v", doc.Duration)
			}
			segments := []video.Segment{{StartMS: 0, DurationMS: 1250, Text: "Abertura transcrita."}}
			if doc.URL == canonical {
				for i := int64(2); i < 1100; i++ {
					segments = append(segments, video.Segment{
						StartMS: i * 1100, DurationMS: 1000, Text: strings.Repeat("Exemplo de trecho longo. ", 30),
					})
				}
			}
			segments = append(segments, video.Segment{StartMS: 1221200, DurationMS: 3000, Text: "Checkpoint de áudio."})
			return video.Transcript{Language: "pt", Model: "teste", ModelSHA: digest([]byte("modelo")),
				Audio: &video.AudioEvidence{File: "youtube/original.webm", SHA256: digest([]byte("áudio")),
					Size: 42, StartMS: 0, EndMS: 1300000, Parts: 5}, Segments: segments}, nil
		}

		server.set(inbox, "- https://youtu.be/abcdefghijk?si=share\n- "+canonical+"&t=10s&list=ignored\n- "+fallback+"\n- "+restricted+"\n")
		cycleVideos(true)
		assertVideoState(canonical, "", "COMPLETED")
		assertVideoState(fallback, "", "COMPLETED")
		assertVideoState(restricted, "", "ACCESS_REQUIRED")
		if metadataReads.Load() != 3 || audioRuns.Load() != 4 || captionsRead.Load() != 1 || longAudioRuns.Load() != 2 {
			t.Fatalf("chamadas inesperadas: metadados=%d, áudio=%d, legendas=%d, longo=%d", metadataReads.Load(), audioRuns.Load(), captionsRead.Load(), longAudioRuns.Load())
		}
		if server.get(repoLocation+oldPath) != string(oldBytes) {
			t.Fatal("o checkpoint histórico de legenda foi alterado")
		}
		if got := server.get(notePath(restricted)); got != "" {
			t.Fatalf("fonte restrita publicou nota: %q", got)
		}
		if got := server.get(vaultContent + legacyNote); !strings.Contains(got, "Fallback de legendas") || server.get(notePath(fallback)) != "" {
			t.Fatalf("nota antiga deve ser atualizada no mesmo caminho e declarar o fallback: %q", got)
		}
		metadataBytes := server.get(repoLocation + metadataPath(canonical))
		audioPath := path.Join("sources", sourceID(canonical), fingerprint(videoAudioVersion, app.transcriberVersion, digest([]byte(metadataBytes))), "source-content.json")
		checkpoint := server.get(repoLocation + audioPath)
		var saved sourceContent
		if err := json.Unmarshal([]byte(checkpoint), &saved); err != nil || saved.Source.URL != canonical ||
			saved.Capture.Method != "whisper.cpp" || saved.Capture.Audio == nil || saved.Capture.Audio.EndMS != 1300000 ||
			len(saved.TranscriptParts) < 2 || len(saved.Blocks) != 0 || len(saved.Source.Chapters) != 2 {
			t.Fatalf("captura longa inválida: %s (%v)", checkpoint, err)
		}
		for _, ref := range saved.TranscriptParts {
			if data := server.get(repoLocation + ref.Path); len(data) != ref.Size || digest([]byte(data)) != ref.SHA256 {
				t.Fatalf("parte de transcrição não verificável: %+v", ref)
			}
		}
		manifestPath := path.Join("sources", sourceID(canonical), fingerprint(videoConsolidationVersion, digest([]byte(checkpoint)), ""), "manifest.json")
		var savedManifest manifest
		if err := json.Unmarshal([]byte(server.get(repoLocation+manifestPath)), &savedManifest); err != nil ||
			len(savedManifest.Artifacts) != 2 || savedManifest.Artifacts[0].SHA256 != digest([]byte(checkpoint)) ||
			len(savedManifest.Artifacts[0].Dependencies) != 1+len(saved.TranscriptParts) {
			t.Fatalf("manifesto de vídeo não referencia a transcrição: %+v (%v)", savedManifest, err)
		}
		if got := server.get(notePath(canonical)); !strings.Contains(got, "Trechos da transcrição") ||
			!strings.Contains(got, "[00:20:21.200](<"+canonical+"&t=1221s>) Checkpoints") {
			t.Fatalf("nota sem origem ou capítulos: %q", got)
		}

		server.set(inbox, "- "+canonical+"\n  - Foco: checkpoint\n")
		cycleVideos(true)
		assertVideoState(canonical, "Foco: checkpoint", "COMPLETED")
		if got := server.get(notePath(canonical)); !strings.Contains(got, "00:20:21.200–00:20:24.200") ||
			!strings.Contains(got, "Foco: checkpoint") || metadataReads.Load() != 3 || audioRuns.Load() != 4 || captionsRead.Load() != 1 {
			t.Fatalf("o novo foco não reutilizou integralmente a transcrição: %q", got)
		}

		seedLegacy(legacyReview, "NEEDS_REVIEW", "revisão necessária: áudio indisponível para transcrição local: a duração precisa estar entre 0 e 20 minutos")
		for _, retry := range []struct {
			url   string
			count int
		}{
			{canonical, 0},
			{"https://youtu.be/09876543210", 1},
			{legacyReview, 0},
		} {
			count, err := RetryVideo(ctx, dsn, retry.url)
			if err != nil || count != retry.count {
				t.Fatalf("RetryVideo(%q) = %d, %v, esperava %d", retry.url, count, err, retry.count)
			}
		}
		cycleVideos(false)
		assertVideoState(legacyReview, "", "COMPLETED")
		if got := server.get(notePath(legacyReview)); !strings.Contains(got, "Trechos da transcrição") {
			t.Fatalf("faltou a transcrição do vídeo barrado pela política anterior: %q", got)
		}

		const interrupted = "https://www.youtube.com/watch?v=lkjihgfedcb"
		seedLegacy(interrupted, "PUBLISHING", "")
		pendingNote := "Fontes/Vídeos/" + sourceID(interrupted) + ".md"
		pendingData := "# Nota gravada antes da confirmação do worker\n"
		server.set(vaultContent+pendingNote, pendingData)
		if _, err := db.Exec(ctx, `
			INSERT INTO noema_publication_intents (request_id, note_path, sha256)
			SELECT id, $2, $3 FROM noema_ingestion_requests WHERE source_id = $1`,
			sourceID(interrupted), pendingNote, digest([]byte(pendingData))); err != nil {
			t.Fatal(err)
		}
		server.set(inbox, "- "+interrupted+"\n")
		cycleVideos(true)
		assertVideoState(interrupted, "", "COMPLETED")
		if got := server.get(vaultContent + pendingNote); !strings.Contains(got, "Trechos da transcrição") || server.get(notePath(interrupted)) != "" {
			t.Fatalf("a publicação interrompida deve retomar no mesmo caminho: %q", got)
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
