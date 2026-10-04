package anthropic

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

	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/schema"
)

func input(t *testing.T) *reason.AnalysisInput {
	t.Helper()
	pod := `{"metadata":{"namespace":"db","name":"postgres-0"},"spec":{"containers":[{"name":"pg"}]},` +
		`"status":{"phase":"Running","containerStatuses":[{"name":"pg","restartCount":3,"ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`
	in, err := reason.BuildInput(reason.Request{
		Node: "worker-03",
		Host: schema.HostCapture{
			SMART:       map[string]schema.RawJSON{"/dev/sdb": schema.RawJSON(`{"device":{"name":"/dev/sdb","type":"sat"},"smart_status":{"passed":false}}`)},
			CollectedAt: time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
		},
		Kubernetes: schema.K8sCapture{
			Pods:        []schema.RawJSON{schema.RawJSON(pod)},
			Logs:        map[string]string{"db/postgres-0/pg": "password=hunter2\nPANIC: could not fsync: Input/output error"},
			CollectedAt: time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

const verdictJSON = `{"reasoning":"r","relationship":"causal","rootCause":{"layer":"host","description":"disk failing"},` +
	`"confidence":0.85,"evidence":[{"ref":"smart:/dev/sdb","excerpt":"health FAILED"},` +
	`{"ref":"log:db/postgres-0/pg#L2","excerpt":"Input/output error"}],"nextStep":"Consider replacing the disk."}`

// fakeAPI serves one canned Messages API response and records the request.
func fakeAPI(t *testing.T, stopReason, text, model string) (*httptest.Server, *map[string]any, *http.Header) {
	t.Helper()
	var body map[string]any
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		resp := map[string]any{
			"id": "msg_test", "type": "message", "role": "assistant", "model": model,
			"content":     []any{map[string]any{"type": "text", "text": text}},
			"stop_reason": stopReason,
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 10},
		}
		if stopReason == "refusal" {
			resp["stop_details"] = map[string]any{"type": "refusal", "category": "cyber", "explanation": "declined"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &headers
}

func TestAnalyze(t *testing.T) {
	srv, body, headers := fakeAPI(t, "end_turn", verdictJSON, "claude-opus-5")
	b := New(Config{APIKey: "test-key", BaseURL: srv.URL})

	v, err := b.Analyze(context.Background(), input(t))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if v.Relationship != schema.RelationshipCausal || v.Backend.Provider != "anthropic" {
		t.Errorf("verdict = %+v", v)
	}

	req := *body
	if req["model"] != DefaultModel {
		t.Errorf("model = %v, want %s", req["model"], DefaultModel)
	}
	oc, _ := req["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("structured output not requested: output_config = %v", oc)
	}
	if th, _ := req["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Errorf("thinking = %v, want adaptive", req["thinking"])
	}
	if req["fallbacks"] != "default" {
		t.Errorf("fallbacks = %v, want default", req["fallbacks"])
	}
	if !strings.Contains(headers.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("anthropic-beta = %q", headers.Get("anthropic-beta"))
	}
	system, _ := req["system"].([]any)
	if len(system) != 1 || system[0].(map[string]any)["cache_control"] == nil {
		t.Errorf("system prompt should carry cache_control: %v", req["system"])
	}

	// Only redacted text is sent.
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "hunter2") {
		t.Error("an unredacted secret was sent to the API")
	}
}

// A fallback may serve the request; the verdict records who actually answered.
func TestAnalyzeRecordsServingModel(t *testing.T) {
	srv, _, _ := fakeAPI(t, "end_turn", verdictJSON, "claude-opus-4-8")
	v, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Analyze(context.Background(), input(t))
	if err != nil {
		t.Fatal(err)
	}
	if v.Backend.Model != "claude-opus-4-8" {
		t.Errorf("backend.model = %q, want the serving model", v.Backend.Model)
	}
}

func TestAnalyzeDisableFallbacks(t *testing.T) {
	srv, body, headers := fakeAPI(t, "end_turn", verdictJSON, "claude-opus-5")
	if _, err := New(Config{APIKey: "k", BaseURL: srv.URL, DisableFallbacks: true}).Analyze(context.Background(), input(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*body)["fallbacks"]; ok {
		t.Error("fallbacks sent despite DisableFallbacks")
	}
	if strings.Contains(headers.Get("anthropic-beta"), "fallback") {
		t.Error("fallback beta sent despite DisableFallbacks")
	}
}

func TestAnalyzeErrors(t *testing.T) {
	tests := []struct {
		name, stop, text string
		want             error
	}{
		{"refusal", "refusal", "", reason.ErrRefused},
		{"max tokens", "max_tokens", `{"reasoning":"trunc`, reason.ErrMalformedOutput},
		{"prose instead of JSON", "end_turn", "The disk is broken.", reason.ErrMalformedOutput},
		{"fabricated citation", "end_turn", strings.Replace(verdictJSON, "smart:/dev/sdb", "smart:/dev/sdq", 1), reason.ErrMalformedOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := fakeAPI(t, tt.stop, tt.text, "claude-opus-5")
			v, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Analyze(context.Background(), input(t))
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if v.Node != "" {
				t.Errorf("an error must not come with a verdict: %+v", v)
			}
		})
	}
}

func TestAnalyzeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
	}))
	defer srv.Close()
	if _, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Analyze(context.Background(), input(t)); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %v", err)
	}
}

func TestAnalyzeRejectsUnbuiltInput(t *testing.T) {
	if _, err := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}).Analyze(context.Background(), &reason.AnalysisInput{}); !errors.Is(err, reason.ErrUnbuiltInput) {
		t.Errorf("err = %v", err)
	}
}
