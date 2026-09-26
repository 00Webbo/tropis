package local

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nathanwebb/tropis/pkg/reason"
	"github.com/nathanwebb/tropis/pkg/schema"
)

func input(t *testing.T) *reason.AnalysisInput {
	t.Helper()
	in, err := reason.BuildInput(reason.Request{
		Node: "worker-01",
		Host: schema.HostCapture{
			SMART:       map[string]schema.RawJSON{"/dev/sda": schema.RawJSON(`{"device":{"name":"/dev/sda","type":"sat"},"smart_status":{"passed":true}}`)},
			CollectedAt: time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
		},
		Kubernetes: schema.K8sCapture{
			Logs:        map[string]string{"app/web-1/web": "token=abc123secretvalue connecting"},
			CollectedAt: time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

const answer = `{"reasoning":"nothing connects","relationship":"insufficient_evidence","rootCause":null,` +
	`"confidence":0.5,"evidence":[{"ref":"smart:/dev/sda","excerpt":"health passed"}],"nextStep":"Re-check later."}`

func server(t *testing.T, path string, reply func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		reply(w, got)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestOllama(t *testing.T) {
	srv, got := server(t, "/api/chat", func(w http.ResponseWriter, _ map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "done_reason": "stop"})
	})
	b, err := New(Config{BaseURL: srv.URL, Model: "qwen3:32b"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := b.Analyze(context.Background(), input(t))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if v.Relationship != schema.RelationshipInsufficientEvidence || v.Backend.Provider != "local-ollama" || v.Backend.Model != "qwen3:32b" {
		t.Errorf("verdict = %+v", v)
	}

	req := *got
	if req["format"] == nil {
		t.Error("output schema should be sent as format for constrained decoding")
	}
	if req["stream"] != false {
		t.Errorf("stream = %v", req["stream"])
	}
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "abc123secretvalue") {
		t.Error("unredacted secret sent to the local model")
	}
}

func TestOpenAICompatible(t *testing.T) {
	srv, got := server(t, "/v1/chat/completions", func(w http.ResponseWriter, _ map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop",
		}}})
	})
	b, err := New(Config{BaseURL: srv.URL + "/", Model: "meta-llama/Llama-3.3-70B-Instruct", API: APIOpenAI, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Analyze(context.Background(), input(t)); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	rf, _ := (*got)["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format = %v", rf)
	}
}

func TestLocalErrors(t *testing.T) {
	tests := []struct {
		name  string
		reply func(w http.ResponseWriter, _ map[string]any)
		want  error
	}{
		{"truncated", func(w http.ResponseWriter, _ map[string]any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": `{"reasoning":"cut`}, "done_reason": "length"})
		}, reason.ErrMalformedOutput},
		{"not JSON", func(w http.ResponseWriter, _ map[string]any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": "The disk seems fine."}, "done_reason": "stop"})
		}, reason.ErrMalformedOutput},
		{"server error", func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"model not loaded"}`)
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := server(t, "/api/chat", tt.reply)
			b, _ := New(Config{BaseURL: srv.URL, Model: "m"})
			v, err := b.Analyze(context.Background(), input(t))
			if err == nil {
				t.Fatalf("expected an error, got %+v", v)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Error("missing base URL should be rejected")
	}
	if _, err := New(Config{BaseURL: "http://x", Model: "m", API: "grpc"}); err == nil {
		t.Error("unknown API should be rejected")
	}
}
