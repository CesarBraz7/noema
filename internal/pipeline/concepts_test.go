package pipeline

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/article"
	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/internal/storage/github"
	"github.com/CesarBraz7/noema/schemas"
)

func TestConceptScansDenseSourcesAndKeepsLateEvidence(t *testing.T) {
	article := conceptSource{content: sourceContent{Source: metadata{Title: "Artigo"}}}
	v := conceptSource{content: sourceContent{Source: metadata{Title: "Vídeo", Platform: "youtube", URL: "https://www.youtube.com/watch?v=abcdefghijk"}}}
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("b-%03d", i+1)
		article.content.Blocks = append(article.content.Blocks, block{ID: id, Text: strings.Repeat("Contexto do artigo. ", 50)})
		start := int64(i) * 40_000
		v.content.Blocks = append(v.content.Blocks, block{ID: id, StartMS: &start, Text: strings.Repeat("Fala do vídeo. ", 50)})
	}
	article.content.Blocks[69].Text = "A versão 3.2 mudou a condição de uso."
	at := int64(2_799_000)
	v.visual = []conceptVisualEvidence{{
		observation: gemini.Observation{ID: "v-001", StartMS: &at, Kind: "code", Description: "Diagrama do último capítulo", VisibleText: "exit 42"},
		location:    "video-visual.json", modality: "Observação visual",
	}}
	calls := 0
	seen := make(map[string]int)
	d := conceptDrafter{
		cacheDir: t.TempDir(),
		analyze: func(_ context.Context, _ string, evidence []gemini.ConceptEvidence) (gemini.ConceptDraft, error) {
			calls++
			var points []gemini.ConceptPoint
			for _, input := range evidence {
				seen[input.ID]++
				if input.ID == "1-b-070" || input.ID == "2-v-001" {
					points = append(points, gemini.ConceptPoint{Kind: "claim", Text: input.Text, EvidenceIDs: []string{input.ID}})
				}
			}
			if len(points) == 0 {
				return gemini.ConceptDraft{Status: "insufficient", Reason: "Contexto repetido"}, nil
			}
			return gemini.ConceptDraft{Status: "adequate", Points: points}, nil
		},
		draft: func(_ context.Context, _ string, evidence []gemini.ConceptEvidence, findings []gemini.ConceptPoint) (gemini.ConceptDraft, error) {
			if len(evidence) != 2 || len(findings) != 2 {
				t.Fatalf("a síntese não recebeu ambas as passagens tardias: %#v", evidence)
			}
			return gemini.ConceptDraft{Status: "adequate", Points: []gemini.ConceptPoint{{
				Kind: "inference", Text: "A condição do artigo explica o diagrama.", EvidenceIDs: []string{"1-b-070", "2-v-001"},
			}, {
				Kind: "example", Text: "Exemplo ilustrado no último capítulo.", EvidenceIDs: []string{"2-v-001"},
			}}}, nil
		},
		review: func(_ context.Context, _ string, _ []gemini.ConceptEvidence, draft []gemini.ConceptPoint) (gemini.ConceptDraft, error) {
			draft[0].Text = "A versão 3.2 explica a condição exibida no diagrama."
			return gemini.ConceptDraft{Status: "adequate", Points: draft}, nil
		},
	}
	for range 2 {
		data, err := d.buildConcept(context.Background(), "Tema", []conceptSource{article, v})
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"Como funciona", "Na prática", "141 passagens", "`v-001`", "&t=2799s", "versão 3.2 explica"} {
			if !strings.Contains(string(data), text) {
				t.Errorf("nota sem %q", text)
			}
		}
	}
	if calls < 3 || len(seen) != 141 {
		t.Fatalf("cobertura: %d chamadas, %d passagens", calls, len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("%s analisado %d vezes, esperado 1 com checkpoints", id, count)
		}
	}
}

func TestConceptProposal(t *testing.T) {
	const articleURL = "https://example.com/goroutines"
	const videoURL = "https://www.youtube.com/watch?v=abcdefghijk"
	const vaultRoot = "/repos/owner/vault/contents/"
	const artifactsRoot = "/repos/owner/artifacts/contents/"
	ctx := context.Background()
	validator, err := schemas.New()
	if err != nil {
		t.Fatal(err)
	}
	server := &testGitHub{files: map[string]string{}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	web := newSource("web-"+digest([]byte(articleURL)), articleURL, article.Document{
		URL: articleURL, Title: "Variáveis de laço", Text: "Cada goroutine pode capturar a variável da sua iteração.",
	}, now)
	v := newVideoMetadata(videoSourcePrefix+digest([]byte(videoURL)), video.Capture{
		URL: videoURL, Title: "Vídeo sobre Go", Availability: video.PublicAvailability, Duration: 60,
	}, now)
	v = transcribedSource(v, video.Transcript{Language: "en", Model: "tiny", Segments: []video.Segment{
		{StartMS: 21500, DurationMS: 2000, Text: "Goroutines run functions concurrently."},
	}}, now)

	save := func(location string, value any) []byte {
		t.Helper()
		data, err := encodeArtifact(value)
		if err != nil {
			t.Fatal(err)
		}
		server.set(artifactsRoot+location, string(data))
		return data
	}
	ref := func(kind, location string, data []byte) artifact {
		return artifact{Kind: kind, Path: location, SHA256: digest(data), Size: len(data), Dependencies: []string{}, Validated: true}
	}
	for _, fixture := range []struct {
		folder  string
		content sourceContent
	}{{"Artigos", web}, {"Vídeos", v}} {
		content := fixture.content
		root := path.Join("sources", content.SourceID)
		sourcePath := path.Join(root, fingerprint("capture"), "source-content.json")
		if content.Source.Type == "video" {
			part := transcriptPart{SchemaVersion: schemaVersion, SourceID: content.SourceID, Blocks: content.Blocks}
			data, err := encodeArtifact(part)
			if err != nil {
				t.Fatal(err)
			}
			partPath := transcriptPartPath(sourcePath, 0, digest(data))
			content.TranscriptParts = []artifact{ref(schemas.TranscriptPart, partPath, save(partPath, part))}
			content.Blocks = []block{}
		}
		key := fingerprint("consolidate")
		root = path.Join(root, key)
		manifestPath := path.Join(root, "manifest.json")
		save(manifestPath, manifest{SchemaVersion: schemaVersion, SourceID: content.SourceID,
			Fingerprint: key, PipelineVersion: "excerpts", ExtractorVersion: "test",
			Artifacts: []artifact{
				ref(schemas.Source, sourcePath, save(sourcePath, content)),
				ref(schemas.Knowledge, path.Join(root, "knowledge.json"), []byte("{}")),
			},
		})
		server.set(vaultRoot+path.Join("Fontes", fixture.folder, content.SourceID+".md"), manifestNotePrefix+manifestPath+"`\n")
	}

	calls := 0
	d := conceptDrafter{
		vault:     github.New(httpServer.URL, "owner", "vault", "fake-token"),
		artifacts: github.New(httpServer.URL, "owner", "artifacts", "fake-token"),
		validator: validator,
		draft: func(_ context.Context, topic string, evidence []gemini.ConceptEvidence, _ []gemini.ConceptPoint) (gemini.ConceptDraft, error) {
			calls++
			if len(evidence) == 0 || evidence[0].ID != "1-b-001" {
				t.Fatalf("evidências inesperadas: %#v", evidence)
			}
			points := []gemini.ConceptPoint{{
				Kind: "claim", Text: "A variável capturada precisa corresponder à iteração.", EvidenceIDs: []string{evidence[0].ID},
			}, {
				Kind: "example", Text: "Guarde o valor e confira a saída.", EvidenceIDs: []string{evidence[0].ID},
				Setup: "Dentro de main, importe fmt.", CodeLanguage: "go", Code: "valor := 3\nfmt.Println(valor)", ExpectedResult: "3",
			}}
			if len(evidence) == 2 {
				points = append(points, gemini.ConceptPoint{Kind: "claim", Text: "Goroutines executam chamadas concorrentemente.", EvidenceIDs: []string{evidence[1].ID}})
			}
			if topic == "Tema sem evidência" {
				points[0].EvidenceIDs = []string{"2-b-999"}
			}
			return gemini.ConceptDraft{Status: "adequate", Model: "gemini-test", Points: points}, nil
		},
		review: func(_ context.Context, _ string, _ []gemini.ConceptEvidence, draft []gemini.ConceptPoint) (gemini.ConceptDraft, error) {
			return gemini.ConceptDraft{Status: "adequate", Model: "gemini-test", Points: draft}, nil
		},
	}
	proposal, err := d.propose(ctx, "Goroutines em Go", articleURL, videoURL)
	if err != nil || !proposal.Created || !strings.HasPrefix(proposal.Path, "Revisao/Conceitos/") {
		t.Fatalf("proposta inesperada: %#v, %v", proposal, err)
	}
	note := vaultRoot + proposal.Path
	for _, expected := range []string{"[^1-b-001]", "[^2-b-001]", "Fontes/Artigos/", "Fontes/Vídeos/", "&t=21s", "Manifesto"} {
		if !strings.Contains(server.get(note), expected) {
			t.Errorf("proposta sem %q", expected)
		}
	}
	server.set(note, "Edição durante a revisão")
	again, err := d.propose(ctx, "goroutines  EM GO", articleURL, videoURL)
	if err != nil || again.Created || again.Path != proposal.Path ||
		server.get(note) != "Edição durante a revisão" || calls != 1 {
		t.Fatalf("a repetição deve preservar a revisão, sem chamar o modelo: %#v, %v, chamadas %d", again, err, calls)
	}
	if _, err := d.propose(ctx, "Tema sem evidência", articleURL, videoURL); err == nil || !strings.Contains(err.Error(), "inexistente") {
		t.Fatalf("citação inventada aceita: %v", err)
	}

	for _, url := range []string{articleURL, videoURL} {
		single, err := d.propose(ctx, "Goroutines em Go", url)
		if err != nil || !single.Created || single.Path == proposal.Path {
			t.Fatalf("proposta de uma fonte: %#v, %v", single, err)
		}
		doc := server.get(vaultRoot + single.Path)
		if strings.Contains(doc, "Conexões entre fontes") {
			t.Error("documento de uma fonte ganhou uma conexão artificial")
		}
		for _, expected := range []string{"## Como funciona", "Preparação", "~~~go\nvalor := 3\nfmt.Println(valor)\n~~~", "Resultado esperado** — 3"} {
			if !strings.Contains(doc, expected) {
				t.Errorf("documento independente sem %q", expected)
			}
		}
	}

	legacyTopic := "Nota anterior"
	legacyKey := digest([]byte(strings.ToLower(legacyTopic)))[:16]
	legacyPath := "Revisao/Conceitos/nota-anterior-" + legacyKey + ".md"
	server.set(vaultRoot+legacyPath, "Rascunho editado.\n<"+articleURL+">\n<"+videoURL+">\n")
	before := calls
	legacy, err := d.propose(ctx, legacyTopic, articleURL, videoURL)
	if err != nil || legacy.Created || legacy.Path != legacyPath || calls != before {
		t.Fatalf("proposta antiga não foi reutilizada: %#v, %v, chamadas %d", legacy, err, calls-before)
	}
}
