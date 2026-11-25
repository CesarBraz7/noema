package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/CesarBraz7/noema/internal/storage/github"
	"github.com/CesarBraz7/noema/schemas"
)

type transcriptPart struct {
	SchemaVersion string  `json:"schemaVersion"`
	SourceID      string  `json:"sourceId"`
	Blocks        []block `json:"blocks"`
}

func (w *ingestionWorker) putSource(ctx context.Context, source sourceContent, location string, dependencies []string) (artifact, error) {
	data, err := encodeArtifact(source)
	if err != nil {
		return artifact{}, err
	}
	if len(data) > github.MaxFileSize && source.Source.Type == "video" && len(source.Blocks) != 0 {
		parts, err := w.putTranscriptParts(ctx, source, location)
		if err != nil {
			return artifact{}, err
		}
		source.Blocks, source.TranscriptParts = []block{}, parts
		data, err = encodeArtifact(source)
		if err != nil {
			return artifact{}, err
		}
	}
	return w.putArtifactData(ctx, source.SourceID, schemas.Source, location, data, transcriptDependencies(dependencies, source.TranscriptParts))
}

func (w *ingestionWorker) putTranscriptParts(ctx context.Context, source sourceContent, location string) ([]artifact, error) {
	var refs []artifact
	remaining := source.Blocks
	for len(remaining) != 0 {
		size := len(remaining)
		data, err := encodeArtifact(transcriptPart{schemaVersion, source.SourceID, remaining[:size]})
		for err == nil && len(data) > github.MaxFileSize && size > 1 {
			size /= 2
			data, err = encodeArtifact(transcriptPart{schemaVersion, source.SourceID, remaining[:size]})
		}
		if err != nil {
			return nil, err
		}
		if len(data) > github.MaxFileSize {
			return nil, fmt.Errorf("%w: um bloco de transcrição excede o limite do armazenamento", errReview)
		}
		target := transcriptPartPath(location, len(refs), digest(data))
		ref, err := w.putArtifactData(ctx, source.SourceID, schemas.TranscriptPart, target, data, []string{})
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
		remaining = remaining[size:]
	}
	return refs, nil
}

func (w *ingestionWorker) readTranscriptParts(ctx context.Context, source *sourceContent, location string) error {
	for i, ref := range source.TranscriptParts {
		part, data, err := readTranscriptPart(ctx, w.artifacts, w.validator, source.SourceID, location, i, ref)
		if err != nil {
			return err
		}
		if _, err := w.recordArtifact(ctx, source.SourceID, schemas.TranscriptPart, ref.Path, data, []string{}); err != nil {
			return err
		}
		source.Blocks = append(source.Blocks, part.Blocks...)
	}
	return nil
}

func readTranscriptPart(ctx context.Context, repo *github.Repository, validator schemas.Validator, sourceID, location string, index int, ref artifact) (transcriptPart, []byte, error) {
	if ref.Kind != schemas.TranscriptPart || ref.Path != transcriptPartPath(location, index, ref.SHA256) {
		return transcriptPart{}, nil, fmt.Errorf("%w: referência de transcrição inválida", errReview)
	}
	file, err := repo.Get(ctx, ref.Path, "")
	if err != nil {
		return transcriptPart{}, nil, err
	}
	var part transcriptPart
	if !file.Exists || len(file.Data) != ref.Size || digest(file.Data) != ref.SHA256 ||
		validator.Validate(schemas.TranscriptPart, file.Data) != nil ||
		json.Unmarshal(file.Data, &part) != nil || part.SourceID != sourceID {
		return transcriptPart{}, nil, fmt.Errorf("%w: parte de transcrição inválida em %s", errReview, ref.Path)
	}
	return part, file.Data, nil
}

func transcriptPartPath(sourcePath string, index int, sha string) string {
	return path.Join(path.Dir(sourcePath), fmt.Sprintf("transcript-%06d-%s.json", index, sha))
}

func transcriptDependencies(dependencies []string, parts []artifact) []string {
	for _, part := range parts {
		dependencies = append(dependencies, part.SHA256)
	}
	return dependencies
}
