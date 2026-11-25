package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/instagram"
	"github.com/CesarBraz7/noema/internal/adapters/video"
)

const instagramSourcePrefix = "instagram-"

type instagramExtractor interface {
	Extract(context.Context, string) (instagram.Post, error)
}

func (w *ingestionWorker) captureInstagram(ctx context.Context, req request, root string) (sourceContent, artifact, error) {
	location := path.Join(root, fingerprint(instagram.Version, w.transcriberVersion, req.url), "source-content.json")
	return w.capture(ctx, req, location, "instagram", []string{}, func(ctx context.Context) (sourceContent, error) {
		if w.instagram == nil {
			return sourceContent{}, fmt.Errorf("%w: extrator do Instagram indisponível", errReview)
		}
		post, err := w.instagram.Extract(ctx, req.url)
		if err != nil {
			return sourceContent{}, err
		}
		title := strings.TrimSpace(post.Title)
		if title == "" {
			title = "Publicação do Instagram"
			if post.Author != "" {
				title += " de " + post.Author
			}
		}
		source := sourceContent{
			SchemaVersion: schemaVersion, SourceID: req.sourceID, Media: post.Assets, Blocks: []block{},
			Source: metadata{URL: req.url, Type: "instagram", Platform: "instagram", Format: post.Format, Title: title,
				Author: post.Author, PublishedAt: post.PublishedAt, CapturedAt: time.Now().UTC().Truncate(time.Second)},
			Capture: capture{Status: "complete", Method: post.Method, Missing: []string{}},
		}
		addBlock := func(b block) {
			b.ID = fmt.Sprintf("b-%03d", len(source.Blocks)+1)
			source.Blocks = append(source.Blocks, b)
		}
		if caption := strings.TrimSpace(post.Caption); caption != "" {
			addBlock(block{Kind: "caption", Text: caption})
		}
		shortcode := path.Base(strings.TrimSuffix(req.url, "/"))
		for i, item := range post.Assets {
			if item.File == "" {
				source.Capture.Missing = append(source.Capture.Missing, fmt.Sprintf("Mídia %d indisponível.", item.Position))
				continue
			}
			relative := path.Join("instagram", shortcode, item.File)
			filename, err := filepath.Abs(filepath.Join(w.workDir, filepath.FromSlash(relative)))
			if err != nil {
				return sourceContent{}, err
			}
			source.Media[i].File = relative
			if item.Kind == "image" {
				text, err := instagramOCR(ctx, filename)
				if err != nil {
					if ctx.Err() != nil {
						return sourceContent{}, ctx.Err()
					}
					source.Capture.Missing = append(source.Capture.Missing, fmt.Sprintf("OCR indisponível na imagem %d.", item.Position))
				} else if text != "" {
					addBlock(block{Kind: "ocr_text", Text: text, MediaPosition: item.Position})
				}
				continue
			}
			if w.transcribeFile == nil {
				source.Capture.Missing = append(source.Capture.Missing, fmt.Sprintf("Whisper indisponível para a mídia %d.", item.Position))
				continue
			}
			transcript, err := w.transcribeFile(ctx, filename, relative)
			if errors.Is(err, video.ErrAudioUnavailable) || errors.Is(err, video.ErrNoSpeech) {
				source.Capture.Missing = append(source.Capture.Missing, fmt.Sprintf("Mídia %d sem áudio ou fala reconhecível.", item.Position))
				continue
			}
			if err != nil {
				return sourceContent{}, err
			}
			for _, b := range timedBlocks(transcript.Segments) {
				b.MediaPosition = item.Position
				addBlock(b)
			}
			if len(post.Assets) == 1 {
				source.Capture.TranscriptLanguage, source.Capture.TranscriptModel = transcript.Language, transcript.Model
				source.Capture.TranscriptModelSHA, source.Capture.Audio = transcript.ModelSHA, transcript.Audio
				if transcript.Audio != nil {
					source.Source.DurationSeconds = float64(transcript.Audio.EndMS) / 1000
				}
			}
		}
		if len(source.Blocks) == 0 {
			source.Capture.Missing = append(source.Capture.Missing, "Nenhum texto ou trecho de fala disponível na captura.")
		}
		if len(source.Capture.Missing) != 0 {
			source.Capture.Status = "partial"
		}
		return source, nil
	})
}

func instagramOCR(ctx context.Context, filename string) (string, error) {
	ocrCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ocrCtx, "tesseract", filename, "stdout", "-l", "por+eng")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func writeInstagramMedia(body *strings.Builder, source sourceContent) {
	for _, b := range source.Blocks {
		if b.Kind == "caption" {
			fmt.Fprintf(body, "\n## Legenda da publicação\n\n%s\n", markdown(b.Text))
		}
	}
	body.WriteString("\n## Ordem das mídias\n")
	for _, item := range source.Media {
		kind := map[string]string{"image": "Imagem", "video": "Vídeo", "unknown": "Formato não identificado"}[item.Kind]
		fmt.Fprintf(body, "\n%d. **%s**", item.Position, kind)
		if item.File == "" {
			body.WriteString(" · indisponível\n")
			continue
		}
		body.WriteByte('\n')
		for _, b := range source.Blocks {
			if b.MediaPosition == item.Position && b.Kind == "ocr_text" {
				fmt.Fprintf(body, "   - Texto reconhecido: %s\n", markdown(b.Text))
			}
		}
	}
}
