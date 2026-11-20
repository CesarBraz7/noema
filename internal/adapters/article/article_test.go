package article

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestExtractorBlocksPrivateConnections(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "169.254.169.254:80", "100.64.0.1:80", "[fd00::1]:80", "[::ffff:192.168.1.1]:80"} {
		conn, err := dialPublic(context.Background(), "tcp", address)
		if conn != nil {
			conn.Close()
		}
		if !errors.Is(err, ErrUnsafe) {
			t.Errorf("endereço reservado recusado com erro inesperado %v", err)
		}
	}
}

func TestTrafilaturaCLI(t *testing.T) {
	binary, err := exec.LookPath("trafilatura")
	if err != nil {
		if os.Getenv("NOEMA_REQUIRE_TRAFILATURA") != "" {
			t.Fatal(err)
		}
		t.Skip("instale Trafilatura para validar o extrator real")
	}
	e := New(binary)
	if err := e.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Uma fonte de evidência</title>
			<meta name="author" content="Ana Autora"><meta property="article:published_time" content="2024-05-18">
			</head><body><article><h1>Uma fonte de evidência</h1>
			<p>Uma nota rastreável preserva o contexto do seu artigo. Quem consultar o documento pode voltar ao trecho e ler o argumento completo.</p>
			<p>Uma extração auditável guarda autoria, data e trechos suficientes para uma consulta posterior. Reler o original facilita a avaliação de novas evidências.</p>
			</article></body></html>`))
	}))
	defer fixture.Close()
	reqURL := "https://example.com/artigo"
	e.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		forwarded := req.Clone(req.Context())
		forwarded.URL.Scheme, forwarded.URL.Host = "http", strings.TrimPrefix(fixture.URL, "http://")
		resp, err := fixture.Client().Transport.RoundTrip(forwarded)
		if resp != nil {
			resp.Request = req
		}
		return resp, err
	})
	doc, err := e.Extract(context.Background(), reqURL)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Uma fonte de evidência" || doc.Author != "Ana Autora" || doc.PublishedAt != "2024-05-18" ||
		!strings.Contains(doc.Text, "Quem consultar o documento") || doc.URL != reqURL {
		t.Fatalf("extração incorreta: %#v", doc)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
