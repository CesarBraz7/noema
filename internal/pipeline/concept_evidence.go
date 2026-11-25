package pipeline

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/CesarBraz7/noema/internal/adapters/gemini"
	"github.com/CesarBraz7/noema/internal/storage/local"
	"github.com/CesarBraz7/noema/schemas"
)

const (
	directConceptBytes = 44_000
	conceptWindowBytes = 20_000
	finalConceptBytes  = 280_000
)

type conceptVisualEvidence struct {
	observation gemini.Observation
	location    string
	modality    string
}

type conceptCitation struct {
	source   int
	locator  string
	artifact string
	startMS  *int64
	input    gemini.ConceptEvidence
}

func (d conceptDrafter) loadConceptVisual(ctx context.Context, sourceID string, refs []artifact) ([]conceptVisualEvidence, error) {
	var visual []conceptVisualEvidence
	for _, ref := range refs {
		switch ref.Kind {
		case schemas.Visual, schemas.FrameInspection:
		default:
			continue
		}
		if !isSourceArtifact(ref.Path, sourceID, ref.Kind+".json") {
			return nil, errors.New("referência visual inválida no manifesto")
		}
		file, err := d.artifacts.Get(ctx, ref.Path, "")
		if err != nil {
			return nil, err
		}
		var record struct {
			SourceID string         `json:"sourceId"`
			Response *gemini.Result `json:"response"`
		}
		if !file.Exists || len(file.Data) != ref.Size || digest(file.Data) != ref.SHA256 ||
			d.validator.Validate(ref.Kind, file.Data) != nil || json.Unmarshal(file.Data, &record) != nil ||
			record.SourceID != sourceID {
			return nil, fmt.Errorf("evidência visual inválida em %s", path.Base(ref.Path))
		}
		if record.Response == nil {
			continue
		}
		modality := "Observação visual"
		if ref.Kind == schemas.FrameInspection {
			modality = "Inspeção de frame"
		}
		for _, item := range record.Response.Observations {
			visual = append(visual, conceptVisualEvidence{item, ref.Path, modality})
		}
	}
	return visual, nil
}

func collectConceptEvidence(sources []conceptSource) ([]gemini.ConceptEvidence, map[string]conceptCitation, error) {
	var evidence []gemini.ConceptEvidence
	citations := make(map[string]conceptCitation)
	for index, source := range sources {
		var entries []conceptCitation
		title := fmt.Sprintf("Fonte %d · %s", index+1, source.content.Source.Title)
		for _, item := range source.content.Blocks {
			modality := "Texto do artigo"
			switch item.Kind {
			case "caption":
				modality = "Legenda da publicação"
			case "ocr_text":
				modality = "Texto reconhecido na imagem"
			}
			if item.StartMS != nil {
				modality = "Transcrição"
			}
			if item.MediaPosition > 0 {
				modality += fmt.Sprintf(" (mídia %d)", item.MediaPosition)
			}
			runes := []rune(item.Text)
			const chunkSize = 1800
			for offset := 0; offset < len(runes); offset += chunkSize {
				id := fmt.Sprintf("%d-%s", index+1, item.ID)
				locator := "bloco `" + item.ID + "`"
				if item.MediaPosition > 0 {
					locator = fmt.Sprintf("mídia %d, %s", item.MediaPosition, locator)
				}
				if len(runes) > chunkSize {
					part := 1 + offset/chunkSize
					id += fmt.Sprintf("-p%d", part)
					locator += fmt.Sprintf(", parte %d", part)
				}
				snippet := string(runes[offset:min(offset+chunkSize, len(runes))])
				entries = append(entries, conceptCitation{source: index, locator: locator, startMS: item.StartMS,
					input: gemini.ConceptEvidence{ID: id, Source: title, Modality: modality, Text: snippet}})
			}
		}
		for _, item := range source.visual {
			obs := item.observation
			snippet := obs.Kind + " · " + obs.Description
			if obs.VisibleText != "" {
				snippet += "\nTexto visível: " + obs.VisibleText
			}
			entries = append(entries, conceptCitation{source: index, locator: item.modality + " `" + obs.ID + "`",
				artifact: item.location, startMS: obs.StartMS,
				input: gemini.ConceptEvidence{ID: fmt.Sprintf("%d-%s", index+1, obs.ID), Source: title, Modality: item.modality, Text: snippet}})
		}
		if len(entries) == 0 {
			return nil, nil, fmt.Errorf("a fonte %q não tem evidências capturadas", source.content.Source.Title)
		}
		if len(source.visual) != 0 {
			start := func(c conceptCitation) int64 {
				if c.startMS == nil {
					return -1
				}
				return *c.startMS
			}
			slices.SortStableFunc(entries, func(a, b conceptCitation) int {
				return cmp.Compare(start(a), start(b))
			})
		}
		for _, entry := range entries {
			if _, exists := citations[entry.input.ID]; exists {
				return nil, nil, errors.New("uma evidência tem identificador repetido")
			}
			citations[entry.input.ID] = entry
			evidence = append(evidence, entry.input)
		}
	}
	return evidence, citations, nil
}

func (d conceptDrafter) buildConcept(ctx context.Context, topic string, sources []conceptSource) ([]byte, error) {
	evidence, citations, err := collectConceptEvidence(sources)
	if err != nil {
		return nil, err
	}
	var findings []gemini.ConceptPoint
	input := evidence
	scanned := 0
	if conceptSize(evidence) > directConceptBytes {
		input, findings, scanned, err = d.scanConcept(ctx, topic, evidence, citations)
		if err != nil {
			return nil, err
		}
	}
	draft, err := d.draft(ctx, topic, input, findings)
	if err != nil {
		return nil, err
	}
	available := make(map[string]conceptCitation, len(input))
	for _, item := range input {
		available[item.ID] = citations[item.ID]
	}
	if err := validateConcept(draft, available, true); err != nil {
		return nil, fmt.Errorf("rascunho: %w", err)
	}
	reviewed, err := d.review(ctx, topic, input, draft.Points)
	if err != nil {
		return nil, fmt.Errorf("revisão da proposta: %w", err)
	}
	if err := validateConcept(reviewed, available, true); err != nil {
		return nil, fmt.Errorf("revisão da proposta: %w", err)
	}
	return renderConcept(topic, sources, reviewed, available, len(evidence), scanned), nil
}

func (d conceptDrafter) scanConcept(ctx context.Context, topic string, evidence []gemini.ConceptEvidence, citations map[string]conceptCitation) ([]gemini.ConceptEvidence, []gemini.ConceptPoint, int, error) {
	var findings []gemini.ConceptPoint
	used := make(map[string]bool)
	scanned := 0
	for remaining := evidence; len(remaining) > 0; {
		end, size := 0, 0
		for end < len(remaining) {
			next := conceptSize(remaining[end : end+1])
			if end > 0 && size+next > conceptWindowBytes {
				break
			}
			size += next
			end++
		}
		window := remaining[:end]
		remaining = remaining[end:]
		allowed := make(map[string]conceptCitation, len(window))
		for _, item := range window {
			allowed[item.ID] = citations[item.ID]
		}
		record, err := d.scanConceptWindow(ctx, topic, window, allowed)
		if err != nil {
			return nil, nil, scanned, fmt.Errorf("evidências do trecho %d: %w", scanned+1, err)
		}
		scanned++
		for _, item := range record.Points {
			for _, id := range item.EvidenceIDs {
				used[id] = true
			}
		}
		findings = append(findings, record.Points...)
	}
	selected := make([]gemini.ConceptEvidence, 0, len(used))
	for _, item := range evidence {
		if used[item.ID] {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return nil, nil, scanned, errors.New("os artefatos não apresentam evidências suficientes para o tema")
	}
	data, err := json.Marshal(findings)
	if err != nil {
		return nil, nil, scanned, err
	}
	if conceptSize(selected)+len(data) > finalConceptBytes {
		return nil, nil, scanned, errors.New("as evidências relevantes excedem o limite de uma proposta; divida o tema em conceitos menores")
	}
	return selected, findings, scanned, nil
}

func (d conceptDrafter) scanConceptWindow(ctx context.Context, topic string, window []gemini.ConceptEvidence, allowed map[string]conceptCitation) (gemini.ConceptDraft, error) {
	var record gemini.ConceptDraft
	var checkpoint string
	if d.cacheDir != "" {
		data, err := json.Marshal(window)
		if err != nil {
			return record, err
		}
		key := fingerprint(gemini.ConceptCoverageVersion, d.model, topic, digest(data))
		checkpoint = filepath.Join(d.cacheDir, "concept-evidence", key+".json")
		data, err = os.ReadFile(checkpoint)
		if err == nil {
			if err := json.Unmarshal(data, &record); err != nil {
				return record, err
			}
			return record, validateConcept(record, allowed, false)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return record, err
		}
	}
	var err error
	record, err = d.analyze(ctx, topic, window)
	if err != nil {
		return record, err
	}
	if err := validateConcept(record, allowed, false); err != nil {
		return record, err
	}
	if checkpoint != "" {
		err = local.PutJSON(checkpoint, record)
	}
	return record, err
}

func conceptSize(evidence []gemini.ConceptEvidence) int {
	size := 0
	for _, item := range evidence {
		size += len(item.ID) + len(item.Source) + len(item.Modality) + len(item.Text) + 80
	}
	return size
}
