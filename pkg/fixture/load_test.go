package fixture

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/00Webbo/tropis/pkg/schema"
)

// writeScenario creates a scenario directory containing a fixture and,
// optionally, a label.
func writeScenario(t *testing.T, dir string, f schema.Fixture, l *schema.Label) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	fb, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FixtureFile), fb, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if l != nil {
		lb, err := json.MarshalIndent(l, "", "  ")
		if err != nil {
			t.Fatalf("marshal label: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, LabelFile), lb, 0o644); err != nil {
			t.Fatalf("write label: %v", err)
		}
	}
	return dir
}

func validFixture() schema.Fixture {
	ts := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	return schema.Fixture{
		Version:    schema.FixtureVersion,
		ScenarioID: "disk-realloc-growth-01",
		Variant:    schema.VariantNPDAbsent,
		CapturedAt: ts,
		Node:       "worker-02",
		Environment: schema.Environment{
			Kernel:            "6.8.0-45-generic",
			KubernetesVersion: "v1.31.2",
			DiskModel:         "Samsung SSD 870 EVO 1TB",
			DiskTransport:     "sata",
			NPDPresent:        false,
		},
		Host: schema.HostCapture{
			SMART: map[string]schema.RawJSON{
				"/dev/sda": schema.RawJSON(`{"device":{"name":"/dev/sda"},"smart_status":{"passed":true}}`),
			},
			CollectedAt: ts,
		},
		Kubernetes: schema.K8sCapture{
			CollectedAt: ts,
		},
	}
}

func causalLabel() schema.Label {
	layer := schema.LayerHost
	return schema.Label{
		ScenarioID:     "disk-realloc-growth-01",
		Relationship:   schema.RelationshipCausal,
		RootCauseLayer: &layer,
		FaultType:      "disk.reallocated_growth",
		Notes:          "dm-dust injected read errors under write load.",
	}
}

// The T2 acceptance criterion: a fixture loads with its label present on disk
// but withheld from everything the analysis path can see.
func TestLoadFixtureWithholdsLabel(t *testing.T) {
	dir := t.TempDir()
	label := causalLabel()
	writeScenario(t, dir, validFixture(), &label)

	f, err := LoadFixture(dir)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}

	if f.ScenarioID != "disk-realloc-growth-01" {
		t.Errorf("scenarioId = %q", f.ScenarioID)
	}

	// The returned value must carry no ground truth anywhere in it. Serialise
	// the whole thing and look for label-only content: this catches a field
	// added to Fixture later that happens to carry the answer.
	blob, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{
		"disk.reallocated_growth", // FaultType
		"dm-dust injected",        // Notes
		"rootCauseLayer",
	} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("loaded fixture leaks ground truth %q: %s", leak, blob)
		}
	}
}

// A fixture with no label at all must load identically. Analysis never needs
// one, so its absence is not an error.
func TestLoadFixtureWithoutLabelFile(t *testing.T) {
	dir := t.TempDir()
	writeScenario(t, dir, validFixture(), nil)

	if _, err := LoadFixture(dir); err != nil {
		t.Fatalf("LoadFixture without a label should succeed, got: %v", err)
	}
}

// The structural guarantee, asserted against the source itself: nothing in the
// fixture-loading path may reference the label file or the Label type. If
// someone adds a convenience that loads both, this fails.
func TestLoaderCannotReachLabels(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "load.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse load.go: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			// LabelFile is declared in load.go as a const, so a bare mention
			// in the const block is expected; what must not appear is a use of
			// the Label type or the label-reading function.
			if node.Name == "LoadLabel" || node.Name == "LoadLabelFor" {
				t.Errorf("load.go references %s: the fixture loader must not be able to read labels",
					node.Name)
			}
		case *ast.SelectorExpr:
			if sel, ok := node.X.(*ast.Ident); ok && sel.Name == "schema" {
				if node.Sel.Name == "Label" {
					t.Error("load.go references schema.Label: the fixture loader must carry no ground truth")
				}
			}
		}
		return true
	})
}

// The reverse direction of the same guarantee: the Fixture type must not grow
// a field that holds the answer.
func TestFixtureTypeCarriesNoGroundTruth(t *testing.T) {
	blob, err := json.Marshal(validFixture())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(blob, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, forbidden := range []string{
		"label", "relationship", "rootCause", "rootCauseLayer",
		"faultType", "injection", "groundTruth", "expected",
	} {
		if _, present := generic[forbidden]; present {
			t.Errorf("Fixture has field %q, which would leak ground truth into analysis", forbidden)
		}
	}
}

func TestLoadFixtureErrors(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		_, err := LoadFixture(filepath.Join(t.TempDir(), "nope"))
		if !errors.Is(err, ErrNoFixture) {
			t.Errorf("expected ErrNoFixture, got %v", err)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FixtureFile), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFixture(dir); err == nil {
			t.Error("expected a parse error")
		}
	})

	t.Run("invalid fixture is rejected", func(t *testing.T) {
		dir := t.TempDir()
		f := validFixture()
		f.Host.SMART = nil // v1 captures SMART; without it there is no capture
		writeScenario(t, dir, f, nil)

		_, err := LoadFixture(dir)
		if err == nil {
			t.Fatal("expected validation to reject a fixture with no SMART capture")
		}
		if !strings.Contains(err.Error(), "host.smart") {
			t.Errorf("error should name the offending field, got: %v", err)
		}
	})

	t.Run("unknown fields are rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// A stray "relationship" key is exactly the accident this guards
		// against: ground truth pasted into the fixture file.
		bad := `{"version":"v1alpha1","scenarioId":"x","relationship":"causal"}`
		if err := os.WriteFile(filepath.Join(dir, FixtureFile), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFixture(dir); err == nil {
			t.Error("expected unknown field to be rejected")
		}
	})
}

func TestLoadLabelFor(t *testing.T) {
	dir := t.TempDir()
	label := causalLabel()
	writeScenario(t, dir, validFixture(), &label)

	f, err := LoadFixture(dir)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}

	got, err := LoadLabelFor(Case{Dir: dir, Fixture: f})
	if err != nil {
		t.Fatalf("LoadLabelFor: %v", err)
	}
	if got.Relationship != schema.RelationshipCausal {
		t.Errorf("relationship = %q", got.Relationship)
	}
	if got.FaultType != "disk.reallocated_growth" {
		t.Errorf("faultType = %q", got.FaultType)
	}
}

// A label paired with the wrong fixture must be an error, not a silent
// mis-score.
func TestLoadLabelForMismatchedScenario(t *testing.T) {
	dir := t.TempDir()
	label := causalLabel()
	label.ScenarioID = "some-other-scenario"
	writeScenario(t, dir, validFixture(), &label)

	f, err := LoadFixture(dir)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if _, err := LoadLabelFor(Case{Dir: dir, Fixture: f}); err == nil {
		t.Error("expected a mismatched label to be rejected")
	}
}

func TestLoadLabelMissing(t *testing.T) {
	dir := t.TempDir()
	writeScenario(t, dir, validFixture(), nil)

	if _, err := LoadLabel(dir); !errors.Is(err, ErrNoLabel) {
		t.Errorf("expected ErrNoLabel, got %v", err)
	}
}

func TestLoadCorpus(t *testing.T) {
	root := t.TempDir()

	for i, id := range []string{"scenario-b", "scenario-a", "scenario-c"} {
		f := validFixture()
		f.ScenarioID = id
		if i == 2 {
			f.Variant = schema.VariantNPDPresent
			f.Environment.NPDPresent = true
		}
		writeScenario(t, filepath.Join(root, id), f, nil)
	}
	// A directory with no fixture.json is skipped rather than failing.
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(cases) != 3 {
		t.Fatalf("loaded %d cases, want 3", len(cases))
	}

	// Sorted by directory, for determinism.
	want := []string{"scenario-a", "scenario-b", "scenario-c"}
	for i, c := range cases {
		if c.Fixture.ScenarioID != want[i] {
			t.Errorf("case %d = %q, want %q", i, c.Fixture.ScenarioID, want[i])
		}
	}
}

// Synthetic fixtures are useful in development and fatal in a published
// result. PublishableCorpus is the gate.
func TestPublishableCorpusRejectsSynthetic(t *testing.T) {
	root := t.TempDir()

	real := validFixture()
	real.ScenarioID = "real-01"
	writeScenario(t, filepath.Join(root, "real-01"), real, nil)

	fake := validFixture()
	fake.ScenarioID = "dev-01"
	fake.Synthetic = true
	writeScenario(t, filepath.Join(root, "dev-01"), fake, nil)

	if _, err := LoadCorpus(root); err != nil {
		t.Fatalf("LoadCorpus should accept synthetic fixtures: %v", err)
	}

	_, err := PublishableCorpus(root)
	if err == nil {
		t.Fatal("PublishableCorpus must reject a corpus containing synthetic fixtures")
	}
	if !strings.Contains(err.Error(), "dev-01") {
		t.Errorf("error should name the synthetic scenario, got: %v", err)
	}
}

func TestPublishableCorpusAcceptsRealOnly(t *testing.T) {
	root := t.TempDir()
	f := validFixture()
	f.ScenarioID = "real-01"
	writeScenario(t, filepath.Join(root, "real-01"), f, nil)

	cases, err := PublishableCorpus(root)
	if err != nil {
		t.Fatalf("PublishableCorpus: %v", err)
	}
	if len(cases) != 1 {
		t.Errorf("got %d cases, want 1", len(cases))
	}
}
