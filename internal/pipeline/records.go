package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/CesarBraz7/noema/internal/adapters/article"
)

const (
	schemaVersion        = "1.0.0"
	captureVersion       = "article-capture-v1"
	consolidationVersion = "article-excerpts-v1"
)

type sourceContent struct {
	SchemaVersion string     `json:"schemaVersion"`
	SourceID      string     `json:"sourceId"`
	Source        metadata   `json:"source"`
	Capture       capture    `json:"capture"`
	Blocks        []block    `json:"blocks"`
	Media         []struct{} `json:"media"`
}

type metadata struct {
	URL         string    `json:"url"`
	ResolvedURL string    `json:"resolvedUrl,omitempty"`
	Type        string    `json:"type"`
	Platform    string    `json:"platform"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	PublishedAt string    `json:"publishedAt,omitempty"`
	CapturedAt  time.Time `json:"capturedAt"`
}

type capture struct {
	Status  string   `json:"status"`
	Method  string   `json:"method"`
	Missing []string `json:"missing"`
}

type block struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func newSource(id, canonical string, doc article.Document, now time.Time) sourceContent {
	title := strings.TrimSpace(doc.Title)
	if title == "" {
		title = "Artigo web"
	}
	date := doc.PublishedAt
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		date = ""
	}
	source := sourceContent{
		SchemaVersion: schemaVersion, SourceID: id, Media: []struct{}{},
		Source: metadata{
			URL: canonical, ResolvedURL: doc.URL, Type: "article", Platform: "web", Title: title,
			Author: strings.TrimSpace(doc.Author), PublishedAt: date, CapturedAt: now.UTC().Truncate(time.Second),
		},
		Capture: capture{Status: "partial", Method: "trafilatura", Missing: []string{"Imagens e outros elementos não textuais não foram analisados."}},
	}
	for _, paragraph := range strings.Split(doc.Text, "\n") {
		if text := strings.TrimSpace(paragraph); text != "" {
			source.Blocks = append(source.Blocks, block{fmt.Sprintf("b-%03d", len(source.Blocks)+1), "paragraph", text})
		}
	}
	return source
}

type knowledge struct {
	SchemaVersion string    `json:"schemaVersion"`
	SourceID      string    `json:"sourceId"`
	FocusNote     string    `json:"focusNote,omitempty"`
	Title         string    `json:"title"`
	Language      string    `json:"language"`
	Method        string    `json:"method"`
	Coverage      coverage  `json:"coverage"`
	Insights      []insight `json:"insights"`
}

type coverage struct {
	Status     string   `json:"status"`
	Unresolved []string `json:"unresolved"`
}

type insight struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Explanation string     `json:"explanation"`
	Evidence    []evidence `json:"evidence"`
	Confidence  string     `json:"confidence"`
	Limitations []string   `json:"limitations"`
}

type evidence struct {
	SourceBlockID string `json:"sourceBlockId"`
	Locator       string `json:"locator"`
}

func consolidate(source sourceContent, focus string) knowledge {
	result := knowledge{
		SchemaVersion: schemaVersion, SourceID: source.SourceID, Title: source.Source.Title,
		FocusNote: focus, Language: "pt-BR", Method: "extractive",
		Coverage: coverage{Status: "partial", Unresolved: append([]string{
			"A seleção de trechos não constitui uma interpretação ou verificação independente do artigo.",
		}, source.Capture.Missing...)},
	}
	focusWords := make(map[string]bool)
	for _, word := range words(focus) {
		if len([]rune(word)) >= 4 && word != "foco" {
			focusWords[word] = true
		}
	}
	type candidate struct {
		index int
		score int
	}
	candidates := make([]candidate, 0, len(source.Blocks))
	for i, block := range source.Blocks {
		if len(source.Blocks) > 1 && strings.EqualFold(block.Text, source.Source.Title) {
			continue
		}
		seen := make(map[string]bool)
		for _, word := range words(block.Text) {
			if focusWords[word] {
				seen[word] = true
			}
		}
		candidates = append(candidates, candidate{i, len(seen)})
	}
	if len(candidates) == 0 {
		candidates = append(candidates, candidate{})
	}
	// Keep the first substantive block for general context and use focus to rank the rest.
	sort.SliceStable(candidates[1:], func(i, j int) bool { return candidates[i+1].score > candidates[j+1].score })
	candidates = candidates[:min(3, len(candidates))]
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].index < candidates[j].index })
	for i, candidate := range candidates {
		block := source.Blocks[candidate.index]
		text := []rune(block.Text)
		excerpt := block.Text
		if len(text) > 500 {
			excerpt = string(text[:500]) + "…"
		}
		result.Insights = append(result.Insights, insight{
			ID: fmt.Sprintf("insight-%03d", i+1), Kind: "excerpt", Title: fmt.Sprintf("Trecho %d", i+1),
			Explanation: excerpt, Confidence: "supported", Limitations: []string{},
			Evidence: []evidence{{block.ID, fmt.Sprintf("Bloco textual %d", candidate.index+1)}},
		})
	}
	return result
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

type artifact struct {
	Kind         string   `json:"kind"`
	Path         string   `json:"path"`
	SHA256       string   `json:"sha256"`
	Size         int      `json:"size"`
	Dependencies []string `json:"dependencies"`
	Validated    bool     `json:"validated"`
}

type manifest struct {
	SchemaVersion    string     `json:"schemaVersion"`
	SourceID         string     `json:"sourceId"`
	Fingerprint      string     `json:"fingerprint"`
	PipelineVersion  string     `json:"pipelineVersion"`
	ExtractorVersion string     `json:"extractorVersion"`
	Artifacts        []artifact `json:"artifacts"`
}

func fingerprint(parts ...string) string {
	data, _ := json.Marshal(parts)
	return digest(data)
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func note(source sourceContent, knowledge knowledge, manifestPath string) []byte {
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n", markdown(source.Source.Title))
	fmt.Fprintf(&body, "- Fonte <%s>\n", source.Source.URL)
	if source.Source.Author != "" {
		fmt.Fprintf(&body, "- Autoria indicada **%s**\n", markdown(source.Source.Author))
	}
	if source.Source.PublishedAt != "" {
		fmt.Fprintf(&body, "- Data indicada pelo extrator %s\n", source.Source.PublishedAt)
	}
	fmt.Fprintf(&body, "- Captura %s\n", source.Source.CapturedAt.Format(time.RFC3339))
	fmt.Fprintf(&body, "- Manifesto de evidências no repositório de artefatos `%s`\n", manifestPath)
	if knowledge.FocusNote != "" {
		fmt.Fprintf(&body, "\n## Foco da inbox\n\n%s\n", markdown(knowledge.FocusNote))
	}
	body.WriteString("\n## Trechos do artigo\n")
	for _, insight := range knowledge.Insights {
		ref := insight.Evidence[0]
		fmt.Fprintf(&body, "\n> %s\n\n%s · `%s`\n", markdown(insight.Explanation), ref.Locator, ref.SourceBlockID)
	}
	body.WriteString("\n## Limitações\n\n")
	for _, limitation := range knowledge.Coverage.Unresolved {
		fmt.Fprintf(&body, "- %s\n", limitation)
	}
	return []byte(body.String())
}

var markdownEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "~", "\\~", "#", "\\#", "!", "\\!", "=", "\\=")

func markdown(s string) string {
	return markdownEscaper.Replace(strings.Join(strings.Fields(s), " "))
}
