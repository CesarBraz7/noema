package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
	"github.com/CesarBraz7/noema/internal/inbox"
	"github.com/CesarBraz7/noema/internal/storage/github"
	"github.com/CesarBraz7/noema/schemas"
)

type conceptDrafter struct {
	vault, artifacts *github.Repository
	validator        schemas.Validator
	model, cacheDir  string
	analyze          func(context.Context, string, []gemini.ConceptEvidence) (gemini.ConceptDraft, error)
	draft            func(context.Context, string, []gemini.ConceptEvidence, []gemini.ConceptPoint) (gemini.ConceptDraft, error)
	review           func(context.Context, string, []gemini.ConceptEvidence, []gemini.ConceptPoint) (gemini.ConceptDraft, error)
}

type conceptSource struct {
	content      sourceContent
	notePath     string
	manifestPath string
	visual       []conceptVisualEvidence
}

// ConceptProposal identifies the note awaiting review in the vault.
type ConceptProposal struct {
	Path    string
	Created bool
}

// DraftConcept creates a standalone study note from a published source and optional supporting material.
func DraftConcept(ctx context.Context, topic string, urls ...string) (ConceptProposal, error) {
	vault, artifacts, err := repositoriesFromEnv()
	if err != nil {
		return ConceptProposal{}, err
	}
	client, err := gemini.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("NOEMA_GEMINI_MODEL"))
	if err != nil {
		return ConceptProposal{}, err
	}
	validator, err := schemas.New()
	if err != nil {
		return ConceptProposal{}, err
	}
	d := conceptDrafter{
		vault: vault, artifacts: artifacts, validator: validator, model: client.Model,
		cacheDir: envDefault("NOEMA_WORK_DIR", "data"), analyze: client.AnalyzeConceptEvidence,
		draft: client.DraftConcept, review: client.ReviewConcept,
	}
	return d.propose(ctx, topic, urls...)
}

func (d conceptDrafter) propose(ctx context.Context, topic string, urls ...string) (ConceptProposal, error) {
	topic = strings.Join(strings.Fields(topic), " ")
	topicWords := words(topic)
	if len(topicWords) == 0 || len([]rune(topic)) > 160 {
		return ConceptProposal{}, errors.New("informe um tema com até 160 caracteres")
	}
	slug := []rune(strings.Join(topicWords, "-"))
	slug = slug[:min(64, len(slug))]
	topicKey := strings.Join(topicWords, " ")
	if len(urls) == 0 || len(urls) > 2 {
		return ConceptProposal{}, errors.New("informe uma fonte principal e, se quiser, uma fonte complementar")
	}
	entries := make([]inbox.Entry, 0, len(urls))
	keyParts := []string{topicKey}
	for _, raw := range urls {
		parsed := inbox.Parse(raw)
		if len(parsed) != 1 || parsed[0].Focus != "" {
			return ConceptProposal{}, errors.New("informe a URL original de um artigo, vídeo do YouTube ou publicação do Instagram")
		}
		entry := parsed[0]
		if len(entries) > 0 && entry.URL == entries[0].URL {
			return ConceptProposal{}, errors.New("a fonte complementar deve ser diferente da principal")
		}
		entries = append(entries, entry)
		keyParts = append(keyParts, entry.URL)
	}
	const reviewFolder = "Revisao/Conceitos"
	location := path.Join(reviewFolder, string(slug)+"-"+fingerprint(keyParts...)[:16]+".md")
	existing, err := d.vault.Get(ctx, location, "")
	if err != nil || existing.Exists {
		return ConceptProposal{Path: location}, err
	}

	// The first implementation keyed article/video proposals by topic alone.
	if len(entries) == 2 && !entries[0].Video && entries[1].Video {
		legacy := path.Join(reviewFolder, string(slug)+"-"+digest([]byte(topicKey))[:16]+".md")
		file, err := d.vault.Get(ctx, legacy, "")
		if err != nil {
			return ConceptProposal{}, err
		}
		if file.Exists && bytes.Contains(file.Data, []byte("<"+entries[0].URL+">")) &&
			bytes.Contains(file.Data, []byte("<"+entries[1].URL+">")) {
			return ConceptProposal{Path: legacy}, nil
		}
	}

	sources := make([]conceptSource, 0, len(entries))
	for _, entry := range entries {
		source, err := d.loadStudySource(ctx, entry)
		if err != nil {
			return ConceptProposal{}, fmt.Errorf("fonte %d: %w", len(sources)+1, err)
		}
		sources = append(sources, source)
	}
	data, err := d.buildConcept(ctx, topic, sources)
	if err != nil {
		return ConceptProposal{}, err
	}
	err = d.vault.Put(ctx, location, "noema: criar proposta de nota conceitual", data, "")
	if err != nil && !errors.Is(err, github.ErrConflict) {
		return ConceptProposal{}, err
	}
	saved, verifyErr := d.vault.Get(ctx, location, "")
	if verifyErr != nil {
		return ConceptProposal{}, verifyErr
	}
	if !saved.Exists {
		return ConceptProposal{}, errors.New("proposta ainda indisponível para verificação no vault")
	}
	return ConceptProposal{Path: location, Created: err == nil && bytes.Equal(saved.Data, data)}, nil
}

func (d conceptDrafter) loadStudySource(ctx context.Context, entry inbox.Entry) (conceptSource, error) {
	if entry.Instagram {
		return d.loadSource(ctx, entry.URL, instagramSourcePrefix, []string{"Instagram"})
	}
	if !entry.Video {
		return d.loadSource(ctx, entry.URL, "web-", []string{"Artigos"})
	}
	source, err := d.loadSource(ctx, entry.URL, videoSourcePrefix, []string{"Videos", "Vídeos"})
	if err != nil {
		return conceptSource{}, err
	}
	if source.content.Source.Availability != video.PublicAvailability {
		return conceptSource{}, errors.New("a proposta via Gemini exige um vídeo com visibilidade pública confirmada")
	}
	return source, nil
}

var (
	artifactFingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)
	exampleLanguage     = regexp.MustCompile(`^[A-Za-z0-9_+.-]{1,32}$`)
)

func isSourceArtifact(location, id, name string) bool {
	parts := strings.Split(location, "/")
	return len(parts) == 4 && parts[0] == "sources" && parts[1] == id &&
		artifactFingerprint.MatchString(parts[2]) && parts[3] == name
}

func (d conceptDrafter) loadSource(ctx context.Context, canonical, prefix string, folders []string) (conceptSource, error) {
	id := prefix + digest([]byte(canonical))
	var result conceptSource
	var note github.File
	for _, folder := range folders {
		result.notePath = path.Join("Fontes", folder, id+".md")
		var err error
		note, err = d.vault.Get(ctx, result.notePath, "")
		if err != nil {
			return conceptSource{}, err
		}
		if note.Exists {
			break
		}
	}
	if !note.Exists {
		return conceptSource{}, errors.New("a fonte precisa ter uma nota publicada no vault")
	}
	for line := range strings.Lines(string(note.Data)) {
		if target, ok := strings.CutPrefix(strings.TrimSpace(line), manifestNotePrefix); ok {
			if location, closed := strings.CutSuffix(target, "`"); closed {
				result.manifestPath = location
			}
			break
		}
	}
	if !isSourceArtifact(result.manifestPath, id, "manifest.json") {
		return conceptSource{}, errors.New("a nota não referencia um manifesto válido da fonte")
	}
	file, err := d.artifacts.Get(ctx, result.manifestPath, "")
	if err != nil {
		return conceptSource{}, err
	}
	var record manifest
	if !file.Exists || d.validator.Validate(schemas.Manifest, file.Data) != nil ||
		json.Unmarshal(file.Data, &record) != nil || record.SourceID != id ||
		path.Base(path.Dir(result.manifestPath)) != record.Fingerprint {
		return conceptSource{}, errors.New("manifesto de evidências inválido")
	}
	var ref artifact
	for _, item := range record.Artifacts {
		if item.Kind == schemas.Source {
			ref = item
			break
		}
	}
	if !isSourceArtifact(ref.Path, id, "source-content.json") {
		return conceptSource{}, errors.New("referência de captura inválida no manifesto")
	}
	file, err = d.artifacts.Get(ctx, ref.Path, "")
	if err != nil {
		return conceptSource{}, err
	}
	source := &result.content
	expectedType := map[string]string{"web-": "article", videoSourcePrefix: "video", instagramSourcePrefix: "instagram"}[prefix]
	if !file.Exists || len(file.Data) != ref.Size || digest(file.Data) != ref.SHA256 ||
		d.validator.Validate(schemas.Source, file.Data) != nil || json.Unmarshal(file.Data, source) != nil ||
		source.SourceID != id || source.Source.URL != canonical ||
		source.Source.Type != expectedType {
		return conceptSource{}, errors.New("a captura diverge do manifesto ou da URL da fonte")
	}
	for index, partRef := range source.TranscriptParts {
		part, _, err := readTranscriptPart(ctx, d.artifacts, d.validator, id, ref.Path, index, partRef)
		if err != nil {
			return conceptSource{}, err
		}
		source.Blocks = append(source.Blocks, part.Blocks...)
	}
	if source.Source.Type == "video" {
		result.visual, err = d.loadConceptVisual(ctx, id, record.Artifacts)
		if err != nil {
			return conceptSource{}, err
		}
	}
	return result, nil
}

var conceptPointLabels = map[string]string{
	"claim": "Afirmação da fonte", "opinion": "Opinião do autor", "inference": "Inferência",
	"hypothesis": "Hipótese", "example": "Exemplo", "procedure": "Procedimento", "caveat": "Condição",
	"connection": "Conexão", "disagreement": "Divergência",
}

func validateConcept(draft gemini.ConceptDraft, citations map[string]conceptCitation, final bool) error {
	if draft.Status == "insufficient" && len(draft.Points) == 0 && strings.TrimSpace(draft.Reason) != "" {
		if !final {
			return nil
		}
		return fmt.Errorf("evidências insuficientes para o tema: %s", markdown(draft.Reason))
	}
	if draft.Status != "adequate" || len(draft.Points) == 0 || len(draft.Points) > 40 {
		return errors.New("o Gemini não retornou uma proposta conceitual válida")
	}
	explainedByPrimary := false
	for i, point := range draft.Points {
		if conceptPointLabels[point.Kind] == "" || strings.TrimSpace(point.Text) == "" ||
			len([]rune(point.Text)) > 2400 || len(point.EvidenceIDs) == 0 {
			return fmt.Errorf("ponto %d tem tipo, texto ou número de citações inválido (citações: %d)", i+1, len(point.EvidenceIDs))
		}
		if len(point.Setup) > 2400 || len(point.Code) > 6000 || len(point.ExpectedResult) > 2400 ||
			(point.CodeLanguage != "" && (!exampleLanguage.MatchString(point.CodeLanguage) || strings.TrimSpace(point.Code) == "")) ||
			(strings.TrimSpace(point.Code) != "" && strings.TrimSpace(point.ExpectedResult) == "") {
			return errors.New("a proposta contém um exemplo inválido ou sem resultado esperado")
		}
		seen := make(map[string]bool)
		pointSources := make(map[int]bool, 2)
		for _, id := range point.EvidenceIDs {
			citation, ok := citations[id]
			if !ok || seen[id] {
				return errors.New("a proposta cita uma evidência inexistente ou repetida")
			}
			seen[id] = true
			pointSources[citation.source] = true
		}
		if final && (point.Kind == "connection" || point.Kind == "disagreement") && len(pointSources) < 2 {
			return errors.New("uma conexão ou divergência entre fontes precisa citar ambas")
		}
		if pointSources[0] && (point.Kind == "claim" || point.Kind == "inference") {
			explainedByPrimary = true
		}
	}
	if final && !explainedByPrimary {
		return errors.New("a proposta precisa explicar o tema com evidências da fonte principal")
	}
	return nil
}

func renderConcept(topic string, sources []conceptSource, draft gemini.ConceptDraft, citations map[string]conceptCitation, evidenceCount, scanned int) []byte {
	var note strings.Builder
	fmt.Fprintf(&note, "# %s\n\nProposta para revisão · %s\n", markdown(topic), markdown(draft.Model))
	var cited []string
	seen := make(map[string]bool)
	for _, section := range []struct {
		title string
		kinds []string
	}{
		{"Como funciona", []string{"claim", "inference", "opinion"}},
		{"Na prática", []string{"example", "procedure"}},
		{"Condições e limites", []string{"caveat", "hypothesis"}},
		{"Conexões entre fontes", []string{"connection", "disagreement"}},
	} {
		started := false
		for _, point := range draft.Points {
			if !slices.Contains(section.kinds, point.Kind) {
				continue
			}
			if !started {
				fmt.Fprintf(&note, "\n## %s\n", section.title)
				started = true
			}
			note.WriteByte('\n')
			if point.Kind != "claim" {
				fmt.Fprintf(&note, "**%s** — ", conceptPointLabels[point.Kind])
			}
			paragraphs := strings.Split(strings.ReplaceAll(strings.TrimSpace(point.Text), "\r\n", "\n"), "\n\n")
			for i, paragraph := range paragraphs {
				if i > 0 {
					note.WriteString("\n\n")
				}
				note.WriteString(markdown(paragraph))
			}
			for _, id := range point.EvidenceIDs {
				fmt.Fprintf(&note, "[^%s]", id)
				if !seen[id] {
					cited = append(cited, id)
					seen[id] = true
				}
			}
			note.WriteByte('\n')
			writeStudyExample(&note, point)
		}
	}
	note.WriteString("\n## Fontes e cobertura\n")
	fmt.Fprintf(&note, "\nA análise recebeu %d passagens dos artefatos capturados", evidenceCount)
	if scanned > 0 {
		fmt.Fprintf(&note, " em %d partes", scanned)
	}
	fmt.Fprintf(&note, ". A proposta cita %d passagens.\n", len(cited))
	for _, source := range sources {
		meta := source.content.Source
		fmt.Fprintf(&note, "\n- **%s** · [[%s|Nota da fonte]] · <%s>", markdown(meta.Title), strings.TrimSuffix(source.notePath, ".md"), meta.URL)
		if meta.Author != "" {
			fmt.Fprintf(&note, " · %s", markdown(meta.Author))
		}
		if meta.PublishedAt != "" {
			fmt.Fprintf(&note, " · publicação em %s", markdown(meta.PublishedAt))
		}
		fmt.Fprintf(&note, " · captura em %s\n  - Manifesto `%s`\n", meta.CapturedAt.Format(time.RFC3339), source.manifestPath)
		if len(source.visual) > 0 {
			fmt.Fprintf(&note, "  - %d observações visuais registradas a partir de imagens amostradas.\n", len(source.visual))
		}
		for _, gap := range source.content.Capture.Missing {
			if len(source.visual) > 0 && (gap == "A imagem não foi analisada." || gap == "Imagem não foi analisada.") {
				continue
			}
			fmt.Fprintf(&note, "  - %s\n", markdown(gap))
		}
	}
	note.WriteString("\n## Evidências citadas\n\n")
	for _, id := range cited {
		citation := citations[id]
		source := sources[citation.source]
		fmt.Fprintf(&note, "[^%s]: [[%s|Nota da fonte]], %s", id, strings.TrimSuffix(source.notePath, ".md"), citation.locator)
		if citation.startMS != nil {
			start := *citation.startMS
			if source.content.Source.Platform == "youtube" {
				fmt.Fprintf(&note, " · [%s](<%s&t=%ds>)", timestamp(start), source.content.Source.URL, start/1000)
			} else {
				fmt.Fprintf(&note, " · %s", timestamp(start))
			}
		}
		if citation.artifact != "" {
			fmt.Fprintf(&note, " · `%s`", citation.artifact)
		}
		snippet := []rune(citation.input.Text)
		excerpt := markdown(string(snippet[:min(360, len(snippet))]))
		if len(snippet) > 360 {
			excerpt += "…"
		}
		fmt.Fprintf(&note, " — %s\n", excerpt)
	}
	return []byte(note.String())
}

func writeStudyExample(note *strings.Builder, point gemini.ConceptPoint) {
	if setup := markdown(point.Setup); setup != "" {
		fmt.Fprintf(note, "\n**Preparação** — %s\n", setup)
	}
	if code := strings.Trim(point.Code, "\r\n"); strings.TrimSpace(code) != "" {
		code = strings.ReplaceAll(code, "\r\n", "\n")
		// Encapsulate even snippets containing Markdown fences.
		run, longest := 0, 0
		for _, char := range code {
			if char == '~' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("~", max(3, longest+1))
		fmt.Fprintf(note, "\n%s%s\n%s\n%s\n", fence, point.CodeLanguage, code, fence)
	}
	if result := markdown(point.ExpectedResult); result != "" {
		fmt.Fprintf(note, "\n**Resultado esperado** — %s\n", result)
	}
}
