package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanwebb/tropis/pkg/host/prefilter"
	"github.com/nathanwebb/tropis/pkg/reason/mock"
	"github.com/nathanwebb/tropis/pkg/schema"
)

var at = time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)

func sample(t *testing.T, name string) schema.RawJSON {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "host", "smart", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fakeHost map[string]schema.RawJSON

func (f fakeHost) Fetch(_ context.Context, node string) (*schema.HostCapture, error) {
	raw, ok := f[node]
	if !ok {
		return nil, fmt.Errorf("no collector on %s", node)
	}
	return &schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sdb": raw}, CollectedAt: at}, nil
}

type fakeK8s struct {
	nodes     []string
	npd       map[string][]schema.RawJSON
	collected []string
}

func (f *fakeK8s) Nodes(context.Context) ([]string, error) { return f.nodes, nil }
func (f *fakeK8s) NPDConditions(_ context.Context, n string) ([]schema.RawJSON, error) {
	return f.npd[n], nil
}
func (f *fakeK8s) Collect(_ context.Context, n string) (*schema.K8sCapture, error) {
	f.collected = append(f.collected, n)
	pod := `{"metadata":{"namespace":"db","name":"pg-0"},"spec":{"containers":[{"name":"pg"}]},` +
		`"status":{"phase":"Running","containerStatuses":[{"name":"pg","restartCount":4,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`
	return &schema.K8sCapture{
		Pods:        []schema.RawJSON{schema.RawJSON(pod)},
		Logs:        map[string]string{"db/pg-0/pg": "PANIC: could not fsync: Input/output error"},
		CollectedAt: at,
	}, nil
}

type spyWriter struct{ written []schema.Verdict }

func (s *spyWriter) Write(_ context.Context, v schema.Verdict) error {
	s.written = append(s.written, v)
	return nil
}

func newPipeline(t *testing.T, k *fakeK8s, w *spyWriter) *Pipeline {
	return &Pipeline{
		Host: fakeHost{
			"healthy": sample(t, "sata-healthy.json"),
			"failing": sample(t, "sata-failing.json"),
		},
		K8s:        k,
		Backend:    mock.New(),
		Thresholds: prefilter.DefaultThresholds(),
		Writer:     w,
	}
}

// A sweep analyses only what the pre-filter raises, and keeps going past a
// node that fails.
func TestSweep(t *testing.T) {
	k := &fakeK8s{nodes: []string{"failing", "healthy", "no-collector"}}
	w := &spyWriter{}
	outcomes, err := newPipeline(t, k, w).Sweep(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	byNode := map[string]Outcome{}
	for _, o := range outcomes {
		byNode[o.Node] = o
	}

	if f := byNode["failing"]; !f.Raised || f.Verdict == nil || f.Verdict.Relationship != schema.RelationshipCausal {
		t.Errorf("failing node: %+v", f)
	}
	if h := byNode["healthy"]; h.Raised || h.Verdict != nil {
		t.Errorf("a healthy node must not be sent to the model: %+v", h)
	}
	if n := byNode["no-collector"]; n.Error == "" {
		t.Errorf("a node without a collector should record an error: %+v", n)
	}
	if len(k.collected) != 1 || k.collected[0] != "failing" {
		t.Errorf("full Kubernetes capture ran for %v; only candidates should be captured", k.collected)
	}
	if len(w.written) != 1 || w.written[0].Node != "failing" {
		t.Errorf("reports written = %+v", w.written)
	}
	if len(w.written[0].TriggeredBy) == 0 {
		t.Error("the verdict should record which rules raised the node")
	}
}

// An explicit analysis runs whether or not the pre-filter raises the node.
func TestAnalyzeNodeForced(t *testing.T) {
	k := &fakeK8s{nodes: []string{"healthy"}}
	out, err := newPipeline(t, k, &spyWriter{}).AnalyzeNode(context.Background(), "healthy", true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Raised || out.Verdict == nil {
		t.Errorf("forced analysis of an unraised node: %+v", out)
	}
}

// NPD conditions raise a node the SMART rules would not.
func TestNPDRaisesNode(t *testing.T) {
	k := &fakeK8s{
		nodes: []string{"healthy"},
		npd:   map[string][]schema.RawJSON{"healthy": {schema.RawJSON(`{"type":"ReadonlyFilesystem","status":"True"}`)}},
	}
	out, err := newPipeline(t, k, &spyWriter{}).AnalyzeNode(context.Background(), "healthy", false)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Raised || out.Verdict == nil {
		t.Errorf("NPD condition should raise the node: %+v", out)
	}
	if out.TriggeredBy[0] != "npd.ReadonlyFilesystem" {
		t.Errorf("triggeredBy = %v", out.TriggeredBy)
	}
}

type failWriter struct{}

func (failWriter) Write(context.Context, schema.Verdict) error { return errors.New("forbidden") }

func TestWriterFailureIsReported(t *testing.T) {
	p := newPipeline(t, &fakeK8s{nodes: []string{"failing"}}, nil)
	p.Writer = failWriter{}
	out, err := p.AnalyzeNode(context.Background(), "failing", false)
	if err == nil {
		t.Fatal("a failed report write should be an error")
	}
	if out.Verdict == nil {
		t.Error("the verdict should still be returned when writing fails")
	}
}

func TestSweepAll(t *testing.T) {
	k := &fakeK8s{nodes: []string{"healthy"}}
	w := &spyWriter{}
	outcomes, err := newPipeline(t, k, w).Sweep(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Verdict == nil || len(w.written) != 1 {
		t.Errorf("sweep --all should analyse an unraised node: %+v", outcomes)
	}
}
