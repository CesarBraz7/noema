package video

import (
	"errors"
	"strings"
	"testing"
)

func TestParseWhisperTranscript(t *testing.T) {
	const recording = `{"result":{"language":"pt"},"transcription":[
		{"offsets":{"from":0,"to":1200},"text":" Olá, mundo."},
		{"offsets":{"from":1200,"to":2000},"text":" [BLANK_AUDIO]"},
		{"offsets":{"from":2400,"to":3200},"text":" Outro trecho."}
	]}`
	result, err := parseTranscript(strings.NewReader(recording))
	if err != nil || result.Language != "pt" || len(result.Segments) != 2 {
		t.Fatalf("transcrição inesperada: %+v, %v", result, err)
	}
	if result.Segments[0] != (Segment{StartMS: 0, DurationMS: 1200, Text: "Olá, mundo."}) ||
		result.Segments[1] != (Segment{StartMS: 2400, DurationMS: 800, Text: "Outro trecho."}) {
		t.Fatalf("trechos fora de posição: %+v", result.Segments)
	}
	if _, err := parseTranscript(strings.NewReader(`{"result":{"language":"pt"},"transcription":[]}`)); !errors.Is(err, ErrNoSpeech) {
		t.Fatalf("esperava ausência de fala, obtive %v", err)
	}
}

func TestVideoID(t *testing.T) {
	const id = "abcdefghijk"
	for _, link := range []string{
		"https://www.youtube.com/watch?v=" + id + "&list=ignored",
		"https://youtu.be/" + id + "?si=ignored",
		"https://m.youtube.com/shorts/" + id,
	} {
		got, err := videoID(link)
		if err != nil || got != id {
			t.Errorf("videoID(%q) = %q, %v", link, got, err)
		}
	}
	for _, link := range []string{
		"https://youtube.com.example.org/watch?v=" + id,
		"https://www.youtube.com:1234/watch?v=" + id,
		"https://www.youtube.com/playlist?list=" + id,
		"https://www.youtube.com/watch?v=" + id + "&v=12345678901",
	} {
		if _, err := videoID(link); err == nil {
			t.Errorf("videoID(%q) aceitou URL inválida", link)
		}
	}
}

func TestChooseTrack(t *testing.T) {
	const original = "https://www.youtube.com/api/timedtext?lang=en"
	const manual = "https://www.youtube.com/api/timedtext?lang=pt"
	input := metadata{
		Language: "en",
		Subtitles: map[string][]subtitleFormat{
			"pt": {{Ext: "json3", URL: manual}},
		},
		AutomaticCaptions: map[string][]subtitleFormat{
			"en-orig": {{Ext: "json3", URL: original}},
			"pt":      {{Ext: "json3", URL: original + "&tlang=pt"}},
		},
	}
	target, track := chooseTrack(input)
	if target != manual || track.Automatic || track.Language != "pt" {
		t.Fatalf("esperava a legenda manual em português, obtive %q e %+v", target, track)
	}
	input.Subtitles["pt"][0].URL = "https://www.youtube.com.example.org/api/timedtext"
	target, track = chooseTrack(input)
	if target != original || !track.Automatic || track.Language != "en" {
		t.Fatalf("esperava a legenda automática original, obtive %q e %+v", target, track)
	}
	delete(input.AutomaticCaptions, "en-orig")
	if target, _ = chooseTrack(input); target != "" {
		t.Fatalf("tradução automática selecionada como legenda original: %q", target)
	}
}

func TestParseSegments(t *testing.T) {
	const input = `{"events":[
		{"tStartMs":0,"dDurationMs":1200,"segs":[{"utf8":"Olá,"},{"utf8":" mundo!"}]},
		{"tStartMs":1200,"dDurationMs":800,"segs":[{"utf8":"\n"}]},
		{"tStartMs":2000,"dDurationMs":750,"segs":[{"utf8":"Próximo trecho."}]},
		{"tStartMs":2750,"dDurationMs":0}
	]}`
	segments, err := parseSegments(strings.NewReader(input))
	if err != nil || len(segments) != 2 {
		t.Fatalf("esperava dois segmentos, obtive %+v, %v", segments, err)
	}
	if segments[0] != (Segment{StartMS: 0, DurationMS: 1200, Text: "Olá, mundo!"}) {
		t.Errorf("segmento inicial incorreto: %+v", segments[0])
	}
	if segments[1] != (Segment{StartMS: 2000, DurationMS: 750, Text: "Próximo trecho."}) {
		t.Errorf("segundo segmento incorreto: %+v", segments[1])
	}
}
