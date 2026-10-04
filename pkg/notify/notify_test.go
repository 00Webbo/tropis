package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/00Webbo/tropis/pkg/schema"
)

var at = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

func verdict(rel schema.Relationship, layer schema.Layer) schema.Verdict {
	v := schema.Verdict{
		Node: "worker-03", ObservedAt: at, Relationship: rel, Confidence: 0.85,
		Backend:  schema.BackendInfo{Provider: "anthropic", Model: "claude-opus-5", PromptVersion: "v1"},
		Evidence: []schema.Evidence{{Source: "smartctl", Ref: "smart:/dev/sdb#197", Excerpt: "pendingSectors 24"}},
		NextStep: "Consider replacing /dev/sdb.",
	}
	if layer != "" {
		v.RootCause = &schema.RootCause{Layer: layer, Description: "/dev/sdb is failing."}
	}
	return v
}

func ptr(v schema.Verdict) *schema.Verdict { return &v }

func TestPolicy(t *testing.T) {
	causalHost := verdict(schema.RelationshipCausal, schema.LayerHost)
	causalK8s := verdict(schema.RelationshipCausal, schema.LayerKubernetes)
	coincidental := verdict(schema.RelationshipCoincidental, "")
	insufficient := verdict(schema.RelationshipInsufficientEvidence, "")

	tests := []struct {
		name   string
		policy Policy
		prev   *schema.Verdict
		cur    schema.Verdict
		want   Kind // "" means no notification
	}{
		{"first verdict causal", PolicyCausal, nil, causalHost, KindNew},
		{"first verdict coincidental is quiet", PolicyCausal, nil, coincidental, ""},
		{"first verdict coincidental under any", PolicyAny, nil, coincidental, KindNew},
		{"unchanged causal is quiet", PolicyCausal, ptr(causalHost), causalHost, ""},
		{"becomes causal", PolicyCausal, ptr(insufficient), causalHost, KindChanged},
		{"causal layer changes", PolicyCausal, ptr(causalHost), causalK8s, KindChanged},
		{"causal clears", PolicyCausal, ptr(causalHost), coincidental, KindCleared},
		{"non-causal shuffle is quiet", PolicyCausal, ptr(insufficient), coincidental, ""},
		{"non-causal shuffle under any", PolicyAny, ptr(insufficient), coincidental, KindChanged},
		{"unchanged under any is still quiet", PolicyAny, ptr(coincidental), coincidental, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.policy.Evaluate(tt.prev, tt.cur)
			switch {
			case tt.want == "" && c != nil:
				t.Errorf("want no notification, got %s", c.Kind)
			case tt.want != "" && c == nil:
				t.Errorf("want %s, got none", tt.want)
			case c != nil && c.Kind != tt.want:
				t.Errorf("kind = %s, want %s", c.Kind, tt.want)
			}
		})
	}
}

// A model rewording an unchanged verdict must not page anyone.
func TestPolicyIgnoresModelWording(t *testing.T) {
	a := verdict(schema.RelationshipCausal, schema.LayerHost)
	b := a
	b.Confidence = 0.6
	b.RootCause = &schema.RootCause{Layer: schema.LayerHost, Description: "The disk at /dev/sdb is degrading."}
	b.Evidence = nil
	if c := PolicyCausal.Evaluate(&a, b); c != nil {
		t.Errorf("rewording notified: %+v", c)
	}
}

func TestParsePolicy(t *testing.T) {
	if p, err := ParsePolicy(""); err != nil || p != PolicyCausal {
		t.Errorf("default = %q, %v", p, err)
	}
	if _, err := ParsePolicy("everything"); err == nil {
		t.Error("unknown policy should be an error")
	}
}

func capture(t *testing.T, status ...int) (*httptest.Server, *[]map[string]any, *int32) {
	t.Helper()
	var bodies []map[string]any
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		if int(n) <= len(status) {
			w.WriteHeader(status[n-1])
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies, &calls
}

func change() Change {
	return *PolicyCausal.Evaluate(nil, verdict(schema.RelationshipCausal, schema.LayerHost))
}

func TestSlack(t *testing.T) {
	srv, bodies, _ := capture(t)
	if err := NewSlack(srv.URL).Notify(context.Background(), change()); err != nil {
		t.Fatal(err)
	}
	msg := (*bodies)[0]
	if !strings.Contains(msg["text"].(string), "worker-03: causal, host layer") {
		t.Errorf("text = %v", msg["text"])
	}
	raw, _ := json.Marshal(msg["blocks"])
	for _, want := range []string{"Root cause", "/dev/sdb is failing", "smart:/dev/sdb#197", "Next step", "Tropis has taken no action"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("blocks missing %q: %s", want, raw)
		}
	}
}

// Text from a verdict cannot ping a channel or form a link.
func TestSlackEscapes(t *testing.T) {
	c := change()
	c.Current.RootCause.Description = "<!channel> see <https://evil.example|here> & more"
	raw, _ := json.Marshal(SlackMessage(c))
	if strings.Contains(string(raw), "<!channel>") || strings.Contains(string(raw), "<https://evil") {
		t.Errorf("control sequences survived: %s", raw)
	}
}

func TestSlackCleared(t *testing.T) {
	prev := verdict(schema.RelationshipCausal, schema.LayerHost)
	c := PolicyCausal.Evaluate(&prev, verdict(schema.RelationshipInsufficientEvidence, ""))
	raw, _ := json.Marshal(SlackMessage(*c))
	if !strings.Contains(string(raw), "no longer causal") || !strings.Contains(string(raw), "Previously") {
		t.Errorf("cleared message = %s", raw)
	}
}

func TestWebhook(t *testing.T) {
	srv, bodies, _ := capture(t)
	if err := NewWebhook(srv.URL).Notify(context.Background(), change()); err != nil {
		t.Fatal(err)
	}
	m := (*bodies)[0]
	if m["type"] != "tropis.verdict.changed" || m["change"] != "new" || m["node"] != "worker-03" {
		t.Errorf("payload = %v", m)
	}
	if m["verdict"].(map[string]any)["relationship"] != "causal" {
		t.Errorf("verdict = %v", m["verdict"])
	}
}

func TestRetriesTransientFailures(t *testing.T) {
	srv, _, calls := capture(t, 503, 429, 200)
	w := NewWebhook(srv.URL)
	w.p.backoff = time.Millisecond
	if err := w.Notify(context.Background(), change()); err != nil {
		t.Fatalf("should succeed on the third attempt: %v", err)
	}
	if *calls != 3 {
		t.Errorf("calls = %d", *calls)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	srv, _, calls := capture(t, 404)
	w := NewWebhook(srv.URL)
	w.p.backoff = time.Millisecond
	if err := w.Notify(context.Background(), change()); err == nil {
		t.Fatal("a 404 should be an error")
	}
	if *calls != 1 {
		t.Errorf("a 404 was retried: %d calls", *calls)
	}
}

type failing struct{}

func (failing) Notify(context.Context, Change) error { return errors.New("down") }
func (failing) Name() string                         { return "broken" }

// One broken notifier must not silence the others.
func TestMultiTriesEveryNotifier(t *testing.T) {
	srv, bodies, _ := capture(t)
	err := Multi{failing{}, NewWebhook(srv.URL)}.Notify(context.Background(), change())
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("err = %v", err)
	}
	if len(*bodies) != 1 {
		t.Error("the working notifier should still have been called")
	}
}
