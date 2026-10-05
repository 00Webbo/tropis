package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/00Webbo/tropis/pkg/host/prefilter"
	"github.com/00Webbo/tropis/pkg/notify"
	"github.com/00Webbo/tropis/pkg/reason/mock"
	"github.com/00Webbo/tropis/pkg/schema"
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
	nodes []string
	npd   map[string][]schema.RawJSON
	// storageError lists nodes whose workload is crash-looping with a
	// storage error in its log, as does the node "failing". Other nodes run
	// a healthy pod.
	storageError map[string]bool
	collected    []string
}

func (f *fakeK8s) Nodes(context.Context) ([]string, error) { return f.nodes, nil }
func (f *fakeK8s) Collect(_ context.Context, n string) (*schema.K8sCapture, error) {
	f.collected = append(f.collected, n)
	if n != "failing" && !f.storageError[n] {
		pod := `{"metadata":{"namespace":"db","name":"pg-0"},"spec":{"containers":[{"name":"pg"}]},` +
			`"status":{"phase":"Running","containerStatuses":[{"name":"pg","ready":true,"restartCount":0,"state":{"running":{}}}]}}`
		return &schema.K8sCapture{
			Pods:          []schema.RawJSON{schema.RawJSON(pod)},
			NPDConditions: f.npd[n],
			CollectedAt:   at,
		}, nil
	}
	pod := `{"metadata":{"namespace":"db","name":"pg-0"},"spec":{"containers":[{"name":"pg"}]},` +
		`"status":{"phase":"Running","containerStatuses":[{"name":"pg","restartCount":4,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`
	return &schema.K8sCapture{
		Pods:          []schema.RawJSON{schema.RawJSON(pod)},
		Logs:          map[string]string{"db/pg-0/pg": "PANIC: could not fsync: Input/output error"},
		NPDConditions: f.npd[n],
		CollectedAt:   at,
	}, nil
}

// spyWriter stores verdicts like a NodeHealthReport store, and records writes.
type spyWriter struct {
	written []schema.Verdict
	store   map[string]schema.Verdict
}

func (s *spyWriter) Write(_ context.Context, v schema.Verdict) (*schema.Verdict, error) {
	s.written = append(s.written, v)
	if s.store == nil {
		s.store = map[string]schema.Verdict{}
	}
	var prev *schema.Verdict
	if p, ok := s.store[v.Node]; ok {
		prev = &p
	}
	s.store[v.Node] = v
	return prev, nil
}

func (s *spyWriter) Existing(context.Context) (map[string]schema.Verdict, error) {
	out := map[string]schema.Verdict{}
	for k, v := range s.store {
		out[k] = v
	}
	return out, nil
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
	// The Kubernetes capture feeds the pre-filter, so every node with a host
	// capture is collected — once. A raised node is analysed from the same
	// capture, never fetched a second time.
	if !reflect.DeepEqual(k.collected, []string{"failing", "healthy"}) {
		t.Errorf("Kubernetes capture ran for %v; want each reachable node exactly once", k.collected)
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

// A storage error in a failing container raises a node whose SMART data is
// clean: the fault SMART cannot see.
func TestKubernetesStorageErrorRaisesNode(t *testing.T) {
	k := &fakeK8s{nodes: []string{"healthy"}, storageError: map[string]bool{"healthy": true}}
	out, err := newPipeline(t, k, &spyWriter{}).AnalyzeNode(context.Background(), "healthy", false)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Raised || out.Verdict == nil {
		t.Errorf("a storage error in a crash-looping container should raise the node: %+v", out)
	}
	if !reflect.DeepEqual(out.TriggeredBy, []string{"k8s.storage_error"}) {
		t.Errorf("triggeredBy = %v", out.TriggeredBy)
	}
	if len(k.collected) != 1 {
		t.Errorf("Kubernetes capture ran %d times; want once", len(k.collected))
	}
}

// Prefilter is the one function live and eval share; it must combine SMART,
// NPD and Kubernetes triggers.
func TestPrefilterCombinesTriggers(t *testing.T) {
	host := schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sdb": sample(t, "sata-failing.json")}, CollectedAt: at}
	node := `{"metadata":{"name":"n"},"status":{"conditions":[{"type":"DiskPressure","status":"True"}]}}`
	kc := schema.K8sCapture{
		NodeJSON:      schema.RawJSON(node),
		NPDConditions: []schema.RawJSON{schema.RawJSON(`{"type":"ReadonlyFilesystem","status":"True"}`)},
	}
	got := Prefilter(host, kc, prefilter.DefaultThresholds()).TriggeredBy()
	want := map[string]bool{"k8s.disk_pressure": false, "npd.ReadonlyFilesystem": false}
	var smartRule bool
	for _, id := range got {
		if _, ok := want[id]; ok {
			want[id] = true
		}
		if strings.HasPrefix(id, "smart.") {
			smartRule = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("triggeredBy %v lacks %s", got, id)
		}
	}
	if !smartRule {
		t.Errorf("triggeredBy %v lacks a SMART rule", got)
	}
}

type failWriter struct{}

func (failWriter) Write(context.Context, schema.Verdict) (*schema.Verdict, error) {
	return nil, errors.New("forbidden")
}
func (failWriter) Existing(context.Context) (map[string]schema.Verdict, error) { return nil, nil }

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

// recorder is a notifier that remembers what it was told.
type recorder struct{ changes []notify.Change }

func (r *recorder) Notify(_ context.Context, c notify.Change) error {
	r.changes = append(r.changes, c)
	return nil
}
func (r *recorder) Name() string { return "recorder" }

func TestSweepNotifiesOnChangeOnly(t *testing.T) {
	k := &fakeK8s{nodes: []string{"failing"}}
	w := &spyWriter{}
	rec := &recorder{}
	p := newPipeline(t, k, w)
	p.Notifier, p.Policy = rec, notify.PolicyCausal

	// First sweep: a new causal verdict is notified.
	if _, err := p.Sweep(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(rec.changes) != 1 || rec.changes[0].Kind != notify.KindNew {
		t.Fatalf("first sweep notified %+v", rec.changes)
	}
	// Second sweep, same state: nothing new to say.
	outcomes, err := p.Sweep(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.changes) != 1 {
		t.Errorf("an unchanged verdict was notified again: %+v", rec.changes)
	}
	if outcomes[0].Notified != "" {
		t.Errorf("outcome.Notified = %q", outcomes[0].Notified)
	}
}

// A node that recovers stops being raised by the pre-filter. Its causal
// report must still be revisited, or it would stay causal forever.
func TestSweepRevisitsCausalNodeThatRecovered(t *testing.T) {
	k := &fakeK8s{nodes: []string{"healthy"}}
	w := &spyWriter{store: map[string]schema.Verdict{
		"healthy": {Node: "healthy", Relationship: schema.RelationshipCausal,
			RootCause: &schema.RootCause{Layer: schema.LayerHost, Description: "was failing"}},
	}}
	rec := &recorder{}
	p := newPipeline(t, k, w)
	p.Notifier, p.Policy = rec, notify.PolicyCausal

	outcomes, err := p.Sweep(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	o := outcomes[0]
	if o.Raised {
		t.Fatal("test setup: the healthy disk should not be raised")
	}
	if o.Verdict == nil {
		t.Fatal("a node with a causal report must be re-analysed even when not raised")
	}
	if len(rec.changes) != 1 || rec.changes[0].Kind != notify.KindCleared {
		t.Errorf("want a cleared notification, got %+v", rec.changes)
	}
	// A non-causal report does not force re-analysis.
	if _, err := p.Sweep(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(w.written) != 1 {
		t.Errorf("a node with a non-causal report was re-analysed: %d writes", len(w.written))
	}
}
