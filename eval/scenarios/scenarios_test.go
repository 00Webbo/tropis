package scenarios

import (
	"os"
	"path/filepath"
	"slices"
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
		if smartVisible && injected && !slices.Contains(s.Hardware, "real-sata") {
			t.Errorf("%s: expects SMART to change but does not require real-sata hardware", s.ID)
		}
	}
}

// The counters capture demands must be the ones the scenario's synthetic
// archetype grows, so the dev corpus and the real corpus tell the same story.
func TestExpectSMARTMatchesSyntheticArchetype(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	grows := map[string]string{"pending-new": CounterPending, "realloc-growing": CounterReallocated}
	for _, s := range all {
		if len(s.ExpectSMART) == 0 {
			continue
		}
		want, ok := grows[s.Synthetic.SMART]
		if !ok || !slices.Contains(s.ExpectSMART, want) {
			t.Errorf("%s: expectSmart %v does not include what synthetic.smart %q grows", s.ID, s.ExpectSMART, s.Synthetic.SMART)
		}
	}
}

// Exactly the scenarios whose synthetic disk carries stable defects must
// demand them of the real drive, so neither corpus can drift from the other.
func TestScarredSATAMatchesSyntheticArchetype(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if slices.Contains(s.Hardware, "scarred-sata") != (s.Synthetic.SMART == "realloc-stable") {
			t.Errorf("%s: hardware %v does not match synthetic.smart %q", s.ID, s.Hardware, s.Synthetic.SMART)
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
  - id: real-sata-without-expectation
    description: d
    faultType: disk.pending_sectors
    inject: {script: pending-sectors.sh}
    workload: idle
    hardware: [real-sata]
    expected: {relationship: insufficient_evidence}
    synthetic: {smart: pending-new, workload: healthy}
  - id: expectation-without-real-sata
    description: d
    faultType: disk.pending_sectors
    inject: {script: pending-sectors.sh}
    workload: idle
    expectSmart: [pending]
    expected: {relationship: insufficient_evidence}
    synthetic: {smart: pending-new, workload: healthy}
  - id: scarred-and-real
    description: d
    faultType: disk.pending_sectors
    inject: {script: pending-sectors.sh}
    workload: idle
    hardware: [real-sata, scarred-sata]
    expectSmart: [pending_sectors]
    expected: {relationship: insufficient_evidence}
    synthetic: {smart: pending-new, workload: healthy}
  - id: hardware-typo
    description: d
    workload: idle
    hardware: [scared-sata]
    expected: {relationship: coincidental}
    synthetic: {smart: realloc-stable, workload: oom}
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"lower-case", "description", "rootCauseLayer", "shiny", "set together",
		"needs expectSmart", "needs hardware: [real-sata]", "not one of [pending_sectors",
		"contradict each other", `hardware "scared-sata" is not one of`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}
}
