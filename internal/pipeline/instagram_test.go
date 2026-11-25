package pipeline

import (
	"strings"
	"testing"

	"github.com/CesarBraz7/noema/internal/adapters/instagram"
	"github.com/CesarBraz7/noema/schemas"
)

func TestInstagramSourceAndNote(t *testing.T) {
	validator, err := schemas.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"post", "carousel", "reel"} {
		t.Run(format, func(t *testing.T) {
			captured := sourceContent{
				SchemaVersion: schemaVersion, SourceID: "instagram-123",
				Source: metadata{Type: "instagram", Platform: "instagram", Format: format, Title: "Publicação",
					URL: "https://www.instagram.com/p/abcdef/"},
				Capture: capture{Method: "yt-dlp", Status: "partial", Missing: []string{"Mídia indisponível."}},
				Blocks:  []block{},
				Media:   []instagram.Asset{{Position: 1, Kind: "image"}},
			}
			switch format {
			case "carousel":
				captured.Media = append(captured.Media, instagram.Asset{Position: 2, Kind: "image", File: "instagram/abcdef/item-002.jpg", Size: 7, SHA256: digest([]byte("imagem!"))})
				captured.Blocks = append(captured.Blocks, block{ID: "b-001", Kind: "ocr_text", MediaPosition: 2, Text: "Texto do segundo slide"})
			case "reel":
				captured.Media[0].Kind = "video"
			}
			for kind, value := range map[string]any{schemas.Source: captured, schemas.Knowledge: consolidate(captured, "")} {
				data, err := encodeArtifact(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := validator.Validate(kind, data); err != nil {
					t.Fatal(err)
				}
			}
			doc := string(note(captured, consolidate(captured, ""), "manifest.json"))
			if !strings.Contains(doc, "1. **") || !strings.Contains(doc, "indisponível") {
				t.Fatal(doc)
			}
			if format == "carousel" && (!strings.Contains(doc, "2. **Imagem**") || !strings.Contains(doc, "Texto do segundo slide")) {
				t.Fatal(doc)
			}
		})
	}
}
