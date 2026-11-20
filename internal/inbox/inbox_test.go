package inbox

import (
	"reflect"
	"testing"
)

func TestParseDeduplicableEntries(t *testing.T) {
	got := Parse("<!--\n- https://example.com/example\n-->\n" +
		"```markdown\n- https://example.com/example\n```\n" +
		"- [x] <HTTPS://Example.COM:443/hello?utm_source=x&a=1#test>\n" +
		"  - Foco: entender evidências.\n  1. Outra linha de contexto.\n" +
		"- https://www.youtube.com/watch?v=test\n  - Ignorado nesta fase.\n" +
		"https://example.com/hello?a=1\n" +
		"1. [Artigo](https://www.example.com/outra)\n")
	want := []Entry{
		{URL: "https://example.com/hello?a=1", Focus: "Foco: entender evidências.\nOutra linha de contexto."},
		{URL: "https://example.com/hello?a=1"},
		{URL: "https://www.example.com/outra"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entradas = %#v, esperado %#v", got, want)
	}
}

func TestCanonicalURL(t *testing.T) {
	const want = "http://xn--caf-dma.example/a%2Fb?z=1&a=x%2By&a=2"
	got, err := CanonicalURL("http://café.example:80/a%2Fb?z=1&%75tm_source=share&a=x%2By&a=2#heading")
	if err != nil || got != want {
		t.Fatalf("CanonicalURL = %q, %v", got, err)
	}
	for _, raw := range []string{"file:///tmp/a", "https://name:password@example.com", "http://example.com:443/"} {
		if _, err := CanonicalURL(raw); err == nil {
			t.Fatalf("aceitou URL inválida %q", raw)
		}
	}
}
