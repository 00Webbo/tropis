package scenarios

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/00Webbo/tropis/pkg/schema"
)

func TestScenariosLoad(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatalf("scenarios.yaml: %v", err)
	}

	counts := map[schema.Relationship]int{}
	for _, s := range all {
		counts[s.Expected.Relationship]++
	}
	// The pre-registered gate needs at least 20 injected-fault positives, and
	// the corpus is useless without negative controls and genuinely
	// undecidable cases alongside them.
	if counts[schema.RelationshipCausal] < 20 {
		t.Errorf("%d causal scenarios; the gate needs at least 20", counts[schema.RelationshipCausal])
	}
	if counts[schema.RelationshipCoincidental] < 5 {
		t.Errorf("%d coincidental scenarios; negative controls are required", counts[schema.RelationshipCoincidental])
	}
	if counts[schema.RelationshipInsufficientEvidence] < 3 {
		t.Errorf("%d insufficient_evidence scenarios", counts[schema.RelationshipInsufficientEvidence])
	}
}

// Every scenario's injection script and workload manifest must exist.
func TestScenarioReferencesExist(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Inject != nil {
			if _, err := os.Stat(filepath.Join("..", "..", "hack", "inject", s.Inject.Script)); err != nil {
				t.Errorf("%s: injection script %s: %v", s.ID, s.Inject.Script, err)
			}
		}
		if _, err := os.Stat(filepath.Join("workloads", s.Workload+".yaml")); err != nil {
			t.Errorf("%s: workload manifest %s: %v", s.ID, s.Workload, err)
		}
	}
}

// A SMART-visible fault can only be produced on real SATA hardware; a
// scenario claiming one without saying so would be captured wrongly.
func TestSMARTVisibleScenariosNeedRealHardware(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		smartVisible := s.Synthetic.SMART == "pending-new" || s.Synthetic.SMART == "realloc-growing"
		injected := s.Inject != nil && (s.Inject.Script == "pending-sectors.sh" || s.Inject.Script == "degradation.sh")
		if smartVisible && injected && !contains(s.Hardware, "real-sata") {
			t.Errorf("%s: expects SMART to change but does not require real-sata hardware", s.ID)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	bad := `
scenarios:
  - id: Bad_ID
    description: ""
    workload: idle
    expected: {relationship: coincidental, rootCauseLayer: host}
    synthetic: {smart: shiny, workload: healthy}
  - id: dup
    description: d
    faultType: disk.read_errors
    workload: idle
    expected: {relationship: causal}
    synthetic: {smart: healthy, workload: healthy}
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"lower-case", "description", "rootCauseLayer", "shiny", "set together"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
