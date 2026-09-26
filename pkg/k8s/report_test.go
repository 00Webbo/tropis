package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"

	tropis "github.com/00Webbo/tropis/pkg/schema"
)

func sampleVerdict(rel tropis.Relationship) tropis.Verdict {
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	v := tropis.Verdict{
		Node:         "worker-02",
		ObservedAt:   at,
		AgentVersion: "v0.1.0",
		Backend:      tropis.BackendInfo{Provider: "mock", Model: "heuristic-v1", PromptVersion: "v1"},
		Relationship: rel,
		Confidence:   0.8,
		Evidence:     []tropis.Evidence{{Source: "smartctl", CollectedAt: at, Ref: "smart:/dev/sdb#197", Excerpt: "pendingSectors 24"}},
		NextStep:     "Consider replacing /dev/sdb.",
		TriggeredBy:  []string{"smart.pending_sectors"},
		InputDigest:  "sha256:abc",
	}
	if rel == tropis.RelationshipCausal {
		v.RootCause = &tropis.RootCause{Layer: tropis.LayerHost, Description: "failing disk"}
	}
	return v
}

func fakeDynamic() *dynfake.FakeDynamicClient {
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[k8sschema.GroupVersionResource]string{ReportGVR: Kind + "List"})
}

func TestReportWriterCreatesAndUpdates(t *testing.T) {
	dyn := fakeDynamic()
	w := &ReportWriter{Client: dyn}
	ctx := context.Background()

	if err := w.Write(ctx, sampleVerdict(tropis.RelationshipCausal)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	obj, err := dyn.Resource(ReportGVR).Get(ctx, "worker-02", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerdictFromReport(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sampleVerdict(tropis.RelationshipCausal)) {
		t.Errorf("status does not round-trip:\n got %+v\nwant %+v", got, sampleVerdict(tropis.RelationshipCausal))
	}
	if obj.GetLabels()["tropis.io/relationship"] != "causal" {
		t.Errorf("labels = %v", obj.GetLabels())
	}
	if spec, _ := obj.Object["spec"].(map[string]any); spec["nodeName"] != "worker-02" {
		t.Errorf("spec = %v", obj.Object["spec"])
	}

	// A second verdict replaces the first on the same report.
	if err := w.Write(ctx, sampleVerdict(tropis.RelationshipCoincidental)); err != nil {
		t.Fatalf("second write: %v", err)
	}
	obj, _ = dyn.Resource(ReportGVR).Get(ctx, "worker-02", metav1.GetOptions{})
	got, _ = VerdictFromReport(obj)
	if got.Relationship != tropis.RelationshipCoincidental || got.RootCause != nil {
		t.Errorf("status after update = %+v", got)
	}
	if obj.GetLabels()["tropis.io/relationship"] != "coincidental" {
		t.Errorf("label after update = %v", obj.GetLabels())
	}
	list, _ := dyn.Resource(ReportGVR).List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Errorf("reports = %d, want one per node", len(list.Items))
	}
}

// Nothing invalid reaches the cluster.
func TestReportWriterRefusesInvalidVerdict(t *testing.T) {
	v := sampleVerdict(tropis.RelationshipCausal)
	v.Evidence = nil
	if err := (&ReportWriter{Client: fakeDynamic()}).Write(context.Background(), v); err == nil {
		t.Error("an invalid verdict must not be written")
	}
}

// The T11 acceptance criterion: --json and .status serialise from the same
// type, to the same bytes.
func TestStatusIsTheJSONOutput(t *testing.T) {
	v := sampleVerdict(tropis.RelationshipCausal)
	jsonOut, _ := json.Marshal(v)
	status, err := StatusFromVerdict(v)
	if err != nil {
		t.Fatal(err)
	}
	statusOut, _ := json.Marshal(status)

	var a, b any
	_ = json.Unmarshal(jsonOut, &a)
	_ = json.Unmarshal(statusOut, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("--json and .status differ:\n%s\n%s", jsonOut, statusOut)
	}
}

// The committed CRD must be exactly what the generator produces.
func TestCommittedCRDIsCurrent(t *testing.T) {
	want, err := CRDYAML()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "tropis", "crds", "tropis.io_nodehealthreports.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
	if !bytes.HasSuffix(got, want) {
		t.Error("the committed CRD is stale; run go generate ./...")
	}
}

func TestCRDShape(t *testing.T) {
	b, _ := CRDYAML()
	var crd map[string]any
	if err := yaml.Unmarshal(b, &crd); err != nil {
		t.Fatal(err)
	}
	spec := crd["spec"].(map[string]any)
	if spec["scope"] != "Cluster" || spec["group"] != Group {
		t.Errorf("spec = %v", spec)
	}
	version := spec["versions"].([]any)[0].(map[string]any)
	if version["name"] != "v1alpha1" {
		t.Errorf("version = %v", version["name"])
	}
	if _, ok := version["subresources"].(map[string]any)["status"]; !ok {
		t.Error("the status subresource must be enabled")
	}
	props := version["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)["properties"].(map[string]any)
	status := props["status"].(map[string]any)["properties"].(map[string]any)
	for _, f := range []string{"relationship", "rootCause", "confidence", "evidence", "inputDigest"} {
		if _, ok := status[f]; !ok {
			t.Errorf("status schema is missing %s", f)
		}
	}
}
