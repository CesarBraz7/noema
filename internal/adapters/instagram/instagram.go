package instagram

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/storage/local"
)

const Version = "instagram-public-v1"

var (
	ErrAccess = errors.New("Instagram restringiu o acesso público à publicação")
	postPath  = regexp.MustCompile(`^/(?:p|reel|tv)/([A-Za-z0-9_-]{5,32})/?$`)
)

func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return "", errors.New("URL inválida do Instagram")
	}
	switch strings.ToLower(u.Hostname()) {
	case "instagram.com", "www.instagram.com":
	default:
		return "", errors.New("URL inválida do Instagram")
	}
	id := postPath.FindStringSubmatch(u.EscapedPath())
	if id == nil {
		return "", errors.New("use o link individual de uma publicação do Instagram")
	}
	return "https://www.instagram.com/p/" + id[1] + "/", nil
}

type Asset struct {
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	File     string `json:"file,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type Post struct {
	Format, Title, Author, Caption, PublishedAt, Method string
	Assets                                              []Asset
}

type Extractor struct{ root string }

func New(root string) *Extractor { return &Extractor{root: root} }

type media struct {
	position     int
	kind, remote string
}

type discovery struct {
	post  Post
	items []media
}

func (e *Extractor) Extract(ctx context.Context, canonical string) (Post, error) {
	if normalized, err := CanonicalURL(canonical); err != nil || normalized != canonical {
		return Post{}, errors.New("URL não canônica do Instagram")
	}
	id := filepath.Base(strings.TrimSuffix(canonical, "/"))
	root, err := filepath.Abs(filepath.Join(e.root, "instagram", id))
	if err != nil {
		return Post{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Post{}, err
	}
	checkpoint := filepath.Join(root, "capture.json")
	if data, err := os.ReadFile(checkpoint); err == nil {
		var post Post
		if json.Unmarshal(data, &post) != nil || len(post.Assets) == 0 {
			return Post{}, errors.New("checkpoint do Instagram inválido")
		}
		for _, asset := range post.Assets {
			if asset.File == "" {
				continue
			}
			if filepath.Base(asset.File) != asset.File {
				return Post{}, errors.New("caminho de mídia inválido no checkpoint do Instagram")
			}
			file := filepath.Join(root, asset.File)
			info, err := os.Lstat(file)
			if err != nil || !info.Mode().IsRegular() || info.Size() != asset.Size {
				return Post{}, errors.New("mídia do Instagram ausente ou alterada no checkpoint")
			}
		}
		return post, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Post{}, err
	}

	found, err := discoverYTDLP(ctx, canonical, id)
	if err != nil || !slices.ContainsFunc(found.items, func(item media) bool { return item.kind != "unknown" }) {
		fallback, galleryErr := discoverGallery(ctx, canonical)
		if galleryErr == nil {
			found, err = fallback, nil
		} else if errors.Is(galleryErr, ErrAccess) && (errors.Is(err, ErrAccess) || err == nil && found.post.Caption == "") {
			return Post{}, ErrAccess
		}
	}
	if err != nil {
		return Post{}, err
	}
	if len(found.items) == 0 {
		return Post{}, errors.New("extratores não identificaram a mídia da publicação")
	}
	post := found.post
	client := &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, previous []*http.Request) error {
		if len(previous) >= 5 || !mediaURL(req.URL.String()) {
			return errors.New("redirecionamento de mídia do Instagram recusado")
		}
		return nil
	}}
	for _, item := range found.items {
		asset := Asset{Position: item.position, Kind: item.kind}
		stem := fmt.Sprintf("item-%03d", item.position)
		var file string
		if item.kind == "video" && post.Method == "yt-dlp" {
			file, err = downloadVideo(ctx, canonical, root, stem, item.position, post.Format == "carousel")
		} else {
			file, err = downloadMedia(ctx, client, item, root, stem)
		}
		if err != nil {
			if ctx.Err() != nil {
				return Post{}, ctx.Err()
			}
			post.Assets = append(post.Assets, asset)
			continue
		}
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return Post{}, errors.New("mídia vazia ou inválida")
		}
		in, err := os.Open(file)
		if err != nil {
			return Post{}, err
		}
		hash := sha256.New()
		_, hashErr := io.Copy(hash, in)
		if err := errors.Join(hashErr, in.Close()); err != nil {
			return Post{}, err
		}
		asset.File, asset.SHA256, asset.Size = filepath.Base(file), fmt.Sprintf("%x", hash.Sum(nil)), info.Size()
		post.Assets = append(post.Assets, asset)
	}
	if err := local.PutJSON(checkpoint, post); err != nil {
		return Post{}, err
	}
	return post, nil
}

const maxMetadata = 8 << 20

type ytPost struct {
	ID, Title, Uploader, Description, Thumbnail string
	Type                                        string `json:"_type"`
	Date                                        string `json:"upload_date"`
	Formats                                     []json.RawMessage
	Entries                                     []*ytPost
}

func ytCommand(ctx context.Context, arguments ...string) (*exec.Cmd, context.CancelFunc) {
	extractCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-cache-dir", "--use-extractors", "instagram",
		"--socket-timeout", "15", "--retries", "1", "--fragment-retries", "1"}
	return exec.CommandContext(extractCtx, "yt-dlp", append(args, arguments...)...), cancel
}

func discoverYTDLP(ctx context.Context, canonical, id string) (discovery, error) {
	cmd, cancel := ytCommand(ctx, "--ignore-no-formats-error", "--skip-download", "--dump-single-json", "--", canonical)
	defer cancel()
	data, err := cmd.Output()
	if err != nil {
		return discovery{}, extractorError(ctx, err)
	}
	return parseYTDLP(data, id)
}

func parseYTDLP(data []byte, id string) (discovery, error) {
	var raw ytPost
	if len(data) > maxMetadata || json.Unmarshal(data, &raw) != nil || raw.ID != id {
		return discovery{}, errors.New("metadados inválidos da publicação do Instagram")
	}
	post := Post{Format: "post", Title: raw.Title, Author: raw.Uploader, Caption: raw.Description, Method: "yt-dlp"}
	if date, err := time.Parse("20060102", raw.Date); err == nil {
		post.PublishedAt = date.Format(time.DateOnly)
	}
	entries := raw.Entries
	if raw.Type == "playlist" {
		post.Format = "carousel"
	} else {
		if len(raw.Formats) > 0 {
			post.Format = "reel"
		}
		entries = []*ytPost{&raw}
	}
	items := make([]media, 0, len(entries))
	for i, entry := range entries {
		item := media{position: i + 1, kind: "unknown"}
		if entry != nil {
			if len(entry.Formats) > 0 {
				item.kind = "video"
			} else if mediaURL(entry.Thumbnail) && (raw.Type == "playlist" || raw.Type == "image" || raw.Type == "video" && strings.HasPrefix(raw.Title, "Post by ")) {
				item.kind, item.remote = "image", entry.Thumbnail
			}
		}
		items = append(items, item)
	}
	return discovery{post, items}, nil
}

func discoverGallery(ctx context.Context, canonical string) (discovery, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "gallery-dl", "--config-ignore", "--no-input", "-o", "extractor.instagram.videos=merged",
		"-o", "extractor.retry=0", "-o", "extractor.timeout=15", "--dump-json", "--", canonical)
	raw, err := cmd.Output()
	if err != nil {
		return discovery{}, extractorError(ctx, err)
	}
	return parseGallery(raw)
}

func parseGallery(raw []byte) (discovery, error) {
	var messages [][]json.RawMessage
	if len(raw) > maxMetadata || json.Unmarshal(raw, &messages) != nil {
		return discovery{}, errors.New("metadados inválidos do gallery-dl")
	}
	var found discovery
	found.post.Method, found.post.Format = "gallery-dl", "post"
	for _, msg := range messages {
		if len(msg) < 2 {
			continue
		}
		if string(msg[0]) == "-1" {
			if accessMessage(string(msg[1])) {
				return discovery{}, ErrAccess
			}
			continue
		}
		if len(msg) != 3 || string(msg[0]) != "3" {
			continue
		}
		var link string
		var info struct {
			Num, Count                       int
			Description, Username, Extension string
			PostDate                         string `json:"post_date"`
		}
		if json.Unmarshal(msg[1], &link) != nil || json.Unmarshal(msg[2], &info) != nil || !mediaURL(link) {
			continue
		}
		found.post.Caption, found.post.Author = info.Description, info.Username
		if len(info.PostDate) >= 10 {
			if date, err := time.Parse(time.DateOnly, info.PostDate[:10]); err == nil {
				found.post.PublishedAt = date.Format(time.DateOnly)
			}
		}
		kind := "image"
		if info.Extension == "mp4" || info.Extension == "webm" {
			kind = "video"
		}
		found.items = append(found.items, media{max(1, info.Num), kind, link})
		if info.Count > 1 {
			found.post.Format = "carousel"
		}
	}
	if len(found.items) == 0 {
		return discovery{}, errors.New("gallery-dl não retornou mídia do Instagram")
	}
	sort.Slice(found.items, func(i, j int) bool { return found.items[i].position < found.items[j].position })
	if len(found.items) > 1 {
		found.post.Format = "carousel"
	}
	if found.post.Format == "post" && found.items[0].kind == "video" {
		found.post.Format = "reel"
	}
	return found, nil
}

func mediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".cdninstagram.com") || strings.HasSuffix(host, ".fbcdn.net")
}

func downloadMedia(ctx context.Context, client *http.Client, item media, dir, stem string) (string, error) {
	if !mediaURL(item.remote) {
		return "", errors.New("mídia do Instagram sem endereço permitido")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.remote, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("falha ao baixar mídia do Instagram")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mídia do Instagram respondeu com HTTP %d", resp.StatusCode)
	}
	file, err := os.CreateTemp(dir, ".asset-")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	ext := ".mp4"
	var reader io.Reader = resp.Body
	if item.kind == "image" {
		const maxImage = 32 << 20
		reader = io.LimitReader(reader, maxImage+1)
	}
	header := make([]byte, 512)
	n, err := io.ReadFull(reader, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		file.Close()
		return "", err
	}
	if item.kind == "image" {
		switch http.DetectContentType(header[:n]) {
		case "image/jpeg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		case "image/webp":
			ext = ".webp"
		default:
			file.Close()
			return "", errors.New("formato de imagem do Instagram inválido")
		}
	} else if mime := http.DetectContentType(header[:n]); mime != "video/mp4" && mime != "video/webm" {
		file.Close()
		return "", errors.New("formato de vídeo do Instagram inválido")
	} else if mime == "video/webm" {
		ext = ".webm"
	}
	_, err = file.Write(header[:n])
	if err != nil {
		file.Close()
		return "", err
	}
	size, err := io.Copy(file, reader)
	if err := errors.Join(err, file.Close()); err != nil {
		return "", err
	}
	if item.kind == "image" && size+int64(n) > 32<<20 {
		return "", errors.New("imagem do Instagram acima do limite de captura")
	}
	target := filepath.Join(dir, stem+ext)
	return target, os.Rename(file.Name(), target)
}

func downloadVideo(ctx context.Context, canonical, dir, stem string, position int, carousel bool) (string, error) {
	// A separate time limit is unnecessary for the actual download. The worker
	// bounds each attempt and reuses the completed capture.
	args := []string{"--ignore-config", "--no-plugin-dirs", "--no-cache-dir", "--use-extractors", "instagram",
		"--socket-timeout", "15", "--retries", "1", "--fragment-retries", "1", "--no-progress",
		"-f", "b[acodec!=none]/bv+ba/b", "--merge-output-format", "mp4",
		"-o", filepath.Join(dir, stem+".%(ext)s"), "--print", "after_move:filepath", "--no-simulate"}
	if carousel {
		args = append(args, "--playlist-items", strconv.Itoa(position))
	} else {
		args = append(args, "--no-playlist")
	}
	cmd := exec.CommandContext(ctx, "yt-dlp", append(args, "--", canonical)...)
	data, err := cmd.Output()
	if err != nil {
		return "", extractorError(ctx, err)
	}
	file := filepath.Clean(strings.TrimSpace(string(data)))
	if filepath.Dir(file) != dir || !strings.HasPrefix(filepath.Base(file), stem+".") {
		return "", errors.New("caminho de vídeo inesperado")
	}
	return file, nil
}

func accessMessage(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "401 unauthorized") || strings.Contains(message, "403 forbidden") ||
		strings.Contains(message, "login required") || strings.Contains(message, "login is required") ||
		strings.Contains(message, "private") || strings.Contains(message, "not available")
}

func extractorError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && accessMessage(string(exit.Stderr)) {
		return ErrAccess
	}
	return fmt.Errorf("extrator do Instagram falhou: %w", err)
}
