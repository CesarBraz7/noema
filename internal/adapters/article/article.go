// Package article fetches public HTML and passes it to Trafilatura over stdin.
package article

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/CesarBraz7/noema/internal/inbox"
)

const Version = "2.0.0"

const maxHTML = 2 << 20

var (
	ErrAccess      = errors.New("acesso ao artigo restrito")
	ErrUnsupported = errors.New("artigo indisponível ou sem texto extraível")
	ErrUnsafe      = errors.New("endereço do artigo não permitido")
)

type Document struct {
	Title       string `json:"title"`
	Author      string `json:"author"`
	PublishedAt string `json:"date"`
	Text        string `json:"text"`
	URL         string `json:"-"`
}

type Extractor struct {
	client *http.Client
	binary string
}

func New(binary string) *Extractor {
	transport := &http.Transport{
		DialContext:            dialPublic,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		DisableKeepAlives:      true,
	}
	return &Extractor{binary: binary, client: &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return ErrUnsafe
			}
			if _, err := inbox.CanonicalURL(req.URL.String()); err != nil {
				return ErrUnsafe
			}
			return nil
		},
	}}
}

func (e *Extractor) Check(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := e.command(checkCtx, "--version").Output()
	if err != nil || !strings.HasPrefix(string(output), "Trafilatura "+Version+" - ") {
		return fmt.Errorf("instale Trafilatura %s no PATH", Version)
	}
	return nil
}

func (e *Extractor) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.binary, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "PYTHONUTF8=1"}
	return cmd
}

func (e *Extractor) Extract(ctx context.Context, canonical string) (Document, error) {
	var doc Document
	if _, err := inbox.CanonicalURL(canonical); err != nil {
		return doc, ErrUnsafe
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return doc, ErrUnsafe
	}
	req.Header.Set("User-Agent", "Noema/0.1 (article extraction)")
	resp, err := e.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrUnsafe) {
			return doc, ErrUnsafe
		}
		return doc, errors.New("falha na conexão com o artigo")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return doc, ErrAccess
	case http.StatusOK:
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return doc, fmt.Errorf("artigo respondeu com HTTP %d", resp.StatusCode)
	default:
		if resp.StatusCode >= 500 {
			return doc, fmt.Errorf("artigo respondeu com HTTP %d", resp.StatusCode)
		}
		return doc, ErrUnsupported
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentType != "" && contentType != "text/html" && contentType != "application/xhtml+xml" {
		return doc, ErrUnsupported
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTML+1))
	if err != nil {
		return doc, errors.New("falha na leitura do artigo")
	}
	if len(body) > maxHTML {
		return doc, ErrUnsupported
	}

	extractCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := e.command(extractCtx, "--json", "--with-metadata", "--no-comments")
	cmd.Stdin = bytes.NewReader(body)
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return doc, errors.New("falha ao executar Trafilatura")
	}
	if output.Len() == 0 {
		return doc, ErrUnsupported
	}
	if err := json.Unmarshal(output.Bytes(), &doc); err != nil {
		return doc, errors.New("saída JSON inválida do Trafilatura")
	}
	if strings.TrimSpace(doc.Text) == "" {
		return doc, ErrUnsupported
	}
	doc.URL = resp.Request.URL.String()
	return doc, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxHTML {
		return 0, errors.New("saída excede o limite")
	}
	return b.Buffer.Write(p)
}

func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrUnsafe
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, addr := range addresses {
		if !publicIP(addr.Unmap()) {
			return nil, ErrUnsafe
		}
	}
	// Dial the same addresses that passed validation, without resolving them again.
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for _, addr := range addresses {
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("falha ao conectar a IP público: %v", err)
}

var publicIPv6 = netip.MustParsePrefix("2000::/3")
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicIP(addr netip.Addr) bool {
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || (addr.Is6() && !publicIPv6.Contains(addr)) {
		return false
	}
	for _, prefix := range reserved {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
