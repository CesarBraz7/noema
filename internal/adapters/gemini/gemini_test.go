package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CesarBraz7/noema/internal/adapters/video"
)

const testURL = "https://www.youtube.com/watch?v=abcdefghijk"

func TestAnalyzeVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/interactions" ||
			r.Header.Get("x-goog-api-key") != "fake-gemini-key" || r.Header.Get("Api-Revision") != apiRevision {
			t.Error("requisição Gemini não segue o contrato")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct {
			Model             string  `json:"model"`
			Store             *bool   `json:"store"`
			Input             []input `json:"input"`
			SystemInstruction string  `json:"system_instruction"`
			Format            struct {
				Type   string          `json:"type"`
				MIME   string          `json:"mime_type"`
				Schema json.RawMessage `json:"schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "gemini-test" ||
			request.Store == nil || *request.Store || len(request.Input) != 2 ||
			request.Input[0].URI != testURL || request.Input[0].Type != "video" || request.SystemInstruction == "" ||
			request.Format.Type != "text" || request.Format.MIME != "application/json" || !json.Valid(request.Format.Schema) {
			t.Errorf("payload Gemini inesperado: %+v (%v)", request, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply(t, w, `{"observations":[{"kind":"code","startMs":13000,"endMs":15000,"description":"Código em tela."},
			{"kind":"slide","startMs":0,"endMs":10000,"description":"Diagrama introdutório."}],
			"limitations":["Amostragem."],"frameChecks":[{"atMs":14200,"reason":"Linha pequena."}]}`)
	}))
	defer server.Close()
	client, err := New("fake-gemini-key", "gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	client.endpoint = server.URL + "/v1beta/interactions"
	result, err := client.AnalyzeVideo(context.Background(), testURL, 20000)
	if err != nil || result.Model != "gemini-reported" || result.Usage == nil || result.Usage.InputTokens != 12 ||
		len(result.Observations) != 2 || *result.Observations[0].StartMS != 0 || len(result.FrameChecks) != 1 {
		t.Fatalf("resultado = %+v, erro = %v", result, err)
	}
	if _, err := client.AnalyzeVideo(context.Background(), testURL, 12000); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("timestamp fora da fonte passou na validação: %v", err)
	}
	if _, err := client.AnalyzeVideo(context.Background(), "https://example.org/video", 20000); !errors.Is(err, ErrRejected) {
		t.Fatalf("origem alheia ao YouTube não foi rejeitada: %v", err)
	}
}

func TestGeminiQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "81")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := New("fake-gemini-key", "gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	client.endpoint = server.URL
	_, err = client.AnalyzeVideo(context.Background(), testURL, 20000)
	var quota *QuotaError
	if !errors.As(err, &quota) || quota.After != 81*time.Second {
		t.Fatalf("cota inesperada: %v", err)
	}
}

func TestAnalyzeFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []input `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 3 ||
			request.Input[2].Type != "image" || request.Input[2].MIMEType != "image/jpeg" || string(request.Input[2].Data) != "fixture" {
			t.Errorf("entrada da inspeção incorreta: %+v (%v)", request, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply(t, w, `{"observations":[{"kind":"code","startMs":1234,"endMs":1235,"description":"Linha visível."}],"limitations":[],"frameChecks":[]}`)
	}))
	defer server.Close()
	client, err := New("fake-gemini-key", "gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	client.endpoint = server.URL
	frames := []FrameInput{{Frame: video.Frame{AtMS: 1234, JPEG: []byte("fixture")}}}
	if result, err := client.AnalyzeFrames(context.Background(), frames, 20000); err != nil || len(result.Observations) != 1 {
		t.Fatalf("resposta de inspeção = %+v (%v)", result, err)
	}
	frames[0].Frame.AtMS++
	if _, err := client.AnalyzeFrames(context.Background(), frames, 20000); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("evidência de timestamp que não foi enviado passou: %v", err)
	}
}

func TestNativeVideoRejection(t *testing.T) {
	for _, tt := range []struct {
		status  int
		message string
		want    error
	}{
		{http.StatusBadRequest, "The selected model does not support video input.", ErrNativeVideo},
		{http.StatusBadRequest, "Only public YouTube URLs are supported.", ErrRejected},
		{http.StatusBadRequest, "Invalid response_format.schema.", ErrRejected},
		{http.StatusForbidden, "Video input is not supported.", ErrAuthorization},
	} {
		t.Run(tt.message, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": tt.message}})
			}))
			defer server.Close()
			client, err := New("fake-gemini-key", "gemini-test")
			if err != nil {
				t.Fatal(err)
			}
			client.endpoint = server.URL
			if _, err := client.AnalyzeVideo(context.Background(), testURL, 20000); !errors.Is(err, tt.want) {
				t.Fatalf("classificação = %v, esperava %v", err, tt.want)
			}
		})
	}
}

func reply(t *testing.T, w http.ResponseWriter, output string) {
	t.Helper()
	resp := map[string]any{
		"model": "gemini-reported", "status": "completed", "id": "interaction-test",
		"usage": map[string]int{"total_input_tokens": 12, "total_output_tokens": 8, "total_tokens": 20},
		"steps": []any{
			map[string]any{"type": "thought", "content": []any{map[string]string{"type": "text", "text": "Ignore"}}},
			map[string]any{"type": "model_output", "content": []any{map[string]string{"type": "text", "text": output}}},
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Error(err)
	}
}
