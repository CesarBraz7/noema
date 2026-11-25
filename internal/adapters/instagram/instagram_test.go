package instagram

import (
	"errors"
	"testing"
)

func TestCanonicalAliases(t *testing.T) {
	const canonical = "https://www.instagram.com/p/Df7-uWthiRQ/"
	for _, input := range []string{canonical, "https://instagram.com/reel/Df7-uWthiRQ/?igsh=1"} {
		got, err := CanonicalURL(input)
		if err != nil || got != canonical {
			t.Fatalf("%q -> %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"https://instagram.com/stories/usuario/123", "https://instagram.com.evil.test/p/Df7-uWthiRQ/", "https://u@instagram.com/p/Df7-uWthiRQ/"} {
		if _, err := CanonicalURL(input); err == nil {
			t.Fatalf("URL indevida aceita: %q", input)
		}
	}
}

func TestPublicMetadataFormats(t *testing.T) {
	for _, tc := range []struct {
		format, json string
		kinds        []string
	}{
		{"reel", `{"id":"abcdef","_type":"video","title":"Video by perfil","formats":[{}]}`, []string{"video"}},
		{"post", `{"id":"abcdef","_type":"video","title":"Post by perfil","thumbnail":"https://scontent.cdninstagram.com/post.jpg"}`, []string{"image"}},
		{"carousel", `{"id":"abcdef","_type":"playlist","description":"Legenda","entries":[{"thumbnail":"https://scontent.cdninstagram.com/1.jpg"},null,{"formats":[{}]}]}`, []string{"image", "unknown", "video"}},
	} {
		got, err := parseYTDLP([]byte(tc.json), "abcdef")
		if err != nil || got.post.Format != tc.format || len(got.items) != len(tc.kinds) {
			t.Fatalf("%s: %#v %v", tc.format, got, err)
		}
		for i, kind := range tc.kinds {
			if got.items[i].kind != kind || got.items[i].position != i+1 {
				t.Fatalf("%s item %d: %#v", tc.format, i, got.items[i])
			}
		}
	}
}

func TestGalleryOrderAndAccess(t *testing.T) {
	data := []byte(`[[3,"https://scontent.cdninstagram.com/2.jpg",{"num":2,"count":2,"extension":"jpg"}],[3,"https://scontent.cdninstagram.com/1.jpg",{"num":1,"count":2,"extension":"jpg"}]]`)
	got, err := parseGallery(data)
	if err != nil || got.post.Format != "carousel" || len(got.items) != 2 || got.items[0].position != 1 {
		t.Fatalf("%#v %v", got, err)
	}
	if _, err := parseGallery([]byte(`[[-1,{"message":"401 Unauthorized"}]]`)); !errors.Is(err, ErrAccess) {
		t.Fatal(err)
	}
}
