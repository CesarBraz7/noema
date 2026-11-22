package video

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CesarBraz7/noema/internal/storage/local"
)

func TestJoinTranscriptWithVideoOffsets(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "parts.csv")
	list := "part-000000.wav,0.000000,300.000812\n" +
		"part-000001.wav,300.000812,600.002625\n" +
		"part-000002.wav,600.002625,900.004437\n" +
		"part-000003.wav,900.004437,1200.006250\n" +
		"part-000004.wav,1200.006250,1261.000000\n"
	if err := os.WriteFile(index, []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	parts, err := readAudioParts(index)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000001.json", "000002.json", "000003.json"} {
		if err := local.PutJSON(filepath.Join(dir, name), Transcript{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"000000.json", "000004.json"} {
		if err := local.PutJSON(filepath.Join(dir, name), Transcript{Language: "pt",
			Segments: []Segment{{StartMS: 1000, DurationMS: 90000, Text: "Texto transcrito."}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	transcriber := &Transcriber{modelPath: "modelo.bin", modelSHA: strings.Repeat("a", 64)}
	got, err := transcriber.joinTranscript(dir, audioSnapshot{File: "original.webm", SHA256: strings.Repeat("b", 64), Size: 100}, parts, "abcdefghijk")
	if err != nil || len(got.Segments) != 2 || got.Audio == nil {
		t.Fatalf("transcrição unificada: %+v (%v)", got, err)
	}
	if got.Segments[1].StartMS != 1201006 || got.Segments[1].DurationMS != 59994 ||
		got.Audio.EndMS != 1261000 || got.Audio.Parts != 5 || got.Audio.NoSpeech != 3 {
		t.Fatalf("evidência temporal incorreta: %+v, %+v", got.Audio, got.Segments)
	}
}
