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
	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/adapters/video"
)

const (
	schemaVersion                   = "1.0.0"
	captureVersion                  = "article-capture-v1"
	consolidationVersion            = "article-excerpts-v1"
	videoSourcePrefix               = "youtube-"
	videoRequestVersion             = "youtube-audio-first-v1"
	videoMetadataVersion            = "youtube-metadata-v1"
	videoAudioVersion               = "youtube-audio-v2"
	videoCaptionFallbackVersion     = "youtube-caption-fallback-v1"
	videoConsolidationVersion       = "youtube-excerpts-v2"
	videoVisualConsolidationVersion = "youtube-excerpts-visual-v1"
)

type sourceContent struct {
	SchemaVersion   string     `json:"schemaVersion"`
	SourceID        string     `json:"sourceId"`
	Source          metadata   `json:"source"`
	Capture         capture    `json:"capture"`
	Blocks          []block    `json:"blocks"`
	TranscriptParts []artifact `json:"transcriptParts,omitempty"`
	Media           []struct{} `json:"media"`
}

type metadata struct {
	URL             string          `json:"url"`
	ResolvedURL     string          `json:"resolvedUrl,omitempty"`
	Type            string          `json:"type"`
	Platform        string          `json:"platform"`
	Title           string          `json:"title"`
	Author          string          `json:"author,omitempty"`
	PublishedAt     string          `json:"publishedAt,omitempty"`
	CapturedAt      time.Time       `json:"capturedAt"`
	DurationSeconds float64         `json:"durationSeconds,omitempty"`
	Availability    string          `json:"availability,omitempty"`
	Chapters        []video.Chapter `json:"chapters,omitempty"`
}

type capture struct {
	Status             string               `json:"status"`
	Method             string               `json:"method"`
	Missing            []string             `json:"missing"`
	CaptionLanguage    string               `json:"captionLanguage,omitempty"`
	AutomaticCaptions  *bool                `json:"automaticCaptions,omitempty"`
	TranscriptLanguage string               `json:"transcriptLanguage,omitempty"`
	TranscriptModel    string               `json:"transcriptModel,omitempty"`
	TranscriptModelSHA string               `json:"transcriptModelSha256,omitempty"`
	Audio              *video.AudioEvidence `json:"audio,omitempty"`
	CaptionsFallback   bool                 `json:"captionsFallback,omitempty"`
}

type block struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	StartMS *int64 `json:"startMs,omitempty"`
	EndMS   *int64 `json:"endMs,omitempty"`
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
			source.Blocks = append(source.Blocks, block{ID: fmt.Sprintf("b-%03d", len(source.Blocks)+1), Kind: "paragraph", Text: text})
		}
	}
	return source
}

func newVideoMetadata(id string, doc video.Capture, now time.Time) sourceContent {
	return sourceContent{
		SchemaVersion: schemaVersion, SourceID: id, Media: []struct{}{},
		Source: metadata{
			URL: doc.URL, Type: "video", Platform: "youtube", Title: strings.TrimSpace(doc.Title),
			Author: strings.TrimSpace(doc.Channel), PublishedAt: doc.PublishedAt,
			CapturedAt: now.UTC().Truncate(time.Second), DurationSeconds: doc.Duration, Availability: doc.Availability, Chapters: doc.Chapters,
		},
		Capture: capture{Status: "partial", Method: "yt-dlp", Missing: []string{"Áudio original e imagem ainda não foram analisados."}},
		Blocks:  []block{},
	}
}

func captionFallbackSource(source sourceContent, track video.CaptionTrack, now time.Time) sourceContent {
	source.Source.CapturedAt = now.UTC().Truncate(time.Second)
	source.Capture = capture{
		Status: "partial", Method: "yt-dlp", CaptionLanguage: track.Language, AutomaticCaptions: &track.Automatic,
		CaptionsFallback: true, Missing: []string{"O áudio original estava indisponível. A plataforma forneceu as legendas de fallback.",
			"A imagem não foi analisada."},
	}
	source.Blocks = timedBlocks(track.Segments)
	return source
}

func transcribedSource(source sourceContent, transcript video.Transcript, now time.Time) sourceContent {
	source.Source.CapturedAt = now.UTC().Truncate(time.Second)
	source.Capture = capture{
		Status: "partial", Method: "whisper.cpp", TranscriptLanguage: transcript.Language, TranscriptModel: transcript.Model,
		TranscriptModelSHA: transcript.ModelSHA, Audio: transcript.Audio, Missing: []string{"Imagem não foi analisada."},
	}
	if transcript.Audio != nil && transcript.Audio.NoSpeech > 0 {
		source.Capture.Missing = append(source.Capture.Missing,
			fmt.Sprintf("O ASR não reconheceu fala em %d de %d segmentos de áudio.", transcript.Audio.NoSpeech, transcript.Audio.Parts))
	}
	source.Blocks = timedBlocks(transcript.Segments)
	return source
}

func timedBlocks(segments []video.Segment) []block {
	blocks := make([]block, 0, len(segments))
	for i, segment := range segments {
		start, end := segment.StartMS, segment.StartMS+segment.DurationMS
		blocks = append(blocks, block{
			ID: fmt.Sprintf("b-%03d", i+1), Kind: "transcript_segment", Text: segment.Text,
			StartMS: &start, EndMS: &end,
		})
	}
	return blocks
}

type knowledge struct {
	SchemaVersion   string           `json:"schemaVersion"`
	SourceID        string           `json:"sourceId"`
	FocusNote       string           `json:"focusNote,omitempty"`
	Title           string           `json:"title"`
	Language        string           `json:"language"`
	Method          string           `json:"method"`
	Coverage        coverage         `json:"coverage"`
	Insights        []insight        `json:"insights"`
	Visual          *visualKnowledge `json:"visual,omitempty"`
	FrameInspection *visualKnowledge `json:"frameInspection,omitempty"`
}

type visualKnowledge struct {
	Path         string               `json:"artifactPath"`
	SHA256       string               `json:"artifactSha256"`
	Mode         string               `json:"analysisMode"`
	Model        string               `json:"model,omitempty"`
	Observations []gemini.Observation `json:"observations"`
}

func (k *knowledge) addVisual(record visualRecord, ref artifact, inspected bool) {
	if record.Response == nil {
		k.Coverage.Unresolved = append(k.Coverage.Unresolved, record.Unavailable+".")
		return
	}
	k.Visual = &visualKnowledge{
		Path: ref.Path, SHA256: ref.SHA256, Mode: record.Mode, Model: record.Response.Model,
		Observations: record.Response.Observations,
	}
	unresolved := withoutImageLimitation(k.Coverage.Unresolved)
	unresolved = append(unresolved, "A análise visual do Gemini usa imagens amostradas ao longo do vídeo.")
	if !inspected {
		for _, check := range record.Response.FrameChecks {
			unresolved = append(unresolved, fmt.Sprintf("Inspeção visual em %s — %s", timestamp(*check.AtMS), check.Reason))
		}
	}
	if len(record.Response.Observations) == 0 {
		unresolved = append(unresolved, "O Gemini não retornou evidências visuais verificáveis.")
	}
	k.Coverage.Unresolved = append(unresolved, record.Response.Limitations...)
}

func withoutImageLimitation(items []string) []string {
	kept := items[:0]
	for _, item := range items {
		if item != "Imagem não foi analisada." && item != "A imagem não foi analisada." {
			kept = append(kept, item)
		}
	}
	return kept
}

func (k *knowledge) addFrameInspection(record frameInspection, ref artifact) {
	k.FrameInspection = &visualKnowledge{
		Path: ref.Path, SHA256: ref.SHA256, Mode: record.Mode, Observations: []gemini.Observation{},
	}
	for _, check := range record.Missing {
		k.Coverage.Unresolved = append(k.Coverage.Unresolved, fmt.Sprintf("Frame em %s — %s", timestamp(*check.AtMS), check.Reason))
	}
	if record.Failure != "" {
		k.Coverage.Unresolved = append(k.Coverage.Unresolved, record.Failure)
	}
	if record.Response != nil {
		if record.Mode == fallbackFramesMode {
			k.Coverage.Unresolved = append(withoutImageLimitation(k.Coverage.Unresolved),
				"A cobertura visual do fallback se limita aos instantes dos frames selecionados.")
		}
		k.FrameInspection.Model = record.Response.Model
		k.FrameInspection.Observations = record.Response.Observations
		k.Coverage.Unresolved = append(k.Coverage.Unresolved, record.Response.Limitations...)
		if len(record.Response.Observations) == 0 {
			k.Coverage.Unresolved = append(k.Coverage.Unresolved, "Os frames inspecionados não confirmaram detalhes visuais adicionais.")
		}
	}
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
	limitation := "A seleção de trechos não constitui uma interpretação ou verificação independente do artigo."
	switch source.Capture.Method {
	case "yt-dlp":
		limitation = "Os trechos foram selecionados da legenda do vídeo e podem conter erros de transcrição."
	case "whisper.cpp":
		limitation = "Os trechos vieram de transcrição automática do áudio e podem conter erros de reconhecimento."
	}
	result := knowledge{
		SchemaVersion: schemaVersion, SourceID: source.SourceID, Title: source.Source.Title,
		FocusNote: focus, Language: "pt-BR", Method: "extractive",
		Coverage: coverage{Status: "partial", Unresolved: append([]string{limitation}, source.Capture.Missing...)},
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
		locator := fmt.Sprintf("Bloco textual %d", candidate.index+1)
		if block.StartMS != nil && block.EndMS != nil {
			locator = timestamp(*block.StartMS) + "–" + timestamp(*block.EndMS)
		}
		result.Insights = append(result.Insights, insight{
			ID: fmt.Sprintf("insight-%03d", i+1), Kind: "excerpt", Title: fmt.Sprintf("Trecho %d", i+1),
			Explanation: excerpt, Confidence: "supported", Limitations: []string{},
			Evidence: []evidence{{block.ID, locator}},
		})
	}
	return result
}

func timestamp(ms int64) string {
	seconds := ms / 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", seconds/3600, seconds/60%60, seconds%60, ms%1000)
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

func requestFingerprint(sourceID, focus, visualVersion string) string {
	if strings.HasPrefix(sourceID, videoSourcePrefix) {
		if visualVersion != "" {
			return fingerprint(sourceID, focus, videoRequestVersion, visualVersion)
		}
		return fingerprint(sourceID, focus, videoRequestVersion)
	}
	return fingerprint(sourceID, focus)
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
	if source.Capture.AutomaticCaptions != nil {
		kind := "manuais"
		if *source.Capture.AutomaticCaptions {
			kind = "automáticas"
		}
		if source.Capture.CaptionsFallback {
			fmt.Fprintf(&body, "- Fallback de legendas %s do YouTube (%s)\n", kind, markdown(source.Capture.CaptionLanguage))
		} else {
			fmt.Fprintf(&body, "- Legendas %s (%s)\n", kind, markdown(source.Capture.CaptionLanguage))
		}
	}
	if source.Capture.TranscriptLanguage != "" {
		fmt.Fprintf(&body, "- Transcrição do áudio original com Whisper %s (%s)\n", markdown(source.Capture.TranscriptModel), markdown(source.Capture.TranscriptLanguage))
	}
	fmt.Fprintf(&body, "- Captura %s\n", source.Source.CapturedAt.Format(time.RFC3339))
	fmt.Fprintf(&body, "- Manifesto de evidências no repositório de artefatos `%s`\n", manifestPath)
	if knowledge.FocusNote != "" {
		fmt.Fprintf(&body, "\n## Foco da inbox\n\n%s\n", markdown(knowledge.FocusNote))
	}
	if source.Source.Type == "video" && len(source.Source.Chapters) != 0 {
		body.WriteString("\n## Capítulos\n\n")
		for _, chapter := range source.Source.Chapters {
			fmt.Fprintf(&body, "- [%s](<%s&t=%ds>) %s\n", timestamp(chapter.StartMS), source.Source.URL,
				chapter.StartMS/1000, markdown(chapter.Title))
		}
	}
	heading := "Trechos do artigo"
	if source.Source.Type == "video" {
		heading = "Trechos da legenda"
	}
	if source.Capture.Method == "whisper.cpp" {
		heading = "Trechos da transcrição"
	}
	fmt.Fprintf(&body, "\n## %s\n", heading)
	for _, insight := range knowledge.Insights {
		ref := insight.Evidence[0]
		fmt.Fprintf(&body, "\n> %s\n\n%s · `%s`\n", markdown(insight.Explanation), ref.Locator, ref.SourceBlockID)
	}
	if knowledge.Visual != nil {
		writeVisualNote(&body, "Evidências visuais", source.Source.URL, *knowledge.Visual)
	}
	if knowledge.FrameInspection != nil {
		heading := "Inspeção de frames"
		if knowledge.FrameInspection.Mode == fallbackFramesMode {
			heading = "Modo parcial com frames e transcrição"
		}
		writeVisualNote(&body, heading, source.Source.URL, *knowledge.FrameInspection)
	}
	body.WriteString("\n## Limitações\n\n")
	for _, limitation := range knowledge.Coverage.Unresolved {
		fmt.Fprintf(&body, "- %s\n", limitation)
	}
	return []byte(body.String())
}

func writeVisualNote(body *strings.Builder, heading, url string, evidence visualKnowledge) {
	fmt.Fprintf(body, "\n## %s\n\n", heading)
	if evidence.Model != "" {
		fmt.Fprintf(body, "Gemini %s · ", markdown(evidence.Model))
	}
	fmt.Fprintf(body, "`%s`\n", evidence.Path)
	labels := map[string]string{"code": "Código", "slide": "Slide", "diagram": "Diagrama", "table": "Tabela", "demo": "Demonstração", "other": "Imagem"}
	for _, item := range evidence.Observations {
		fmt.Fprintf(body, "\n- [%s](<%s&t=%ds>) **%s** · `%s` · %s\n",
			timestamp(*item.StartMS), url, *item.StartMS/1000, labels[item.Kind], item.ID, markdown(item.Description))
		if item.VisibleText != "" {
			fmt.Fprintf(body, "  - Trecho visível — %s\n", markdown(item.VisibleText))
		}
	}
}

var markdownEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "~", "\\~", "#", "\\#", "!", "\\!", "=", "\\=")

func markdown(s string) string {
	return markdownEscaper.Replace(strings.Join(strings.Fields(s), " "))
}
