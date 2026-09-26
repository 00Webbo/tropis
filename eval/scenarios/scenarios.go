// Package scenarios defines the evaluation scenarios: what to inject, what
// workload to run, and what the right answer is.
//
// scenarios.yaml is the capture plan. On the rig, each definition says which
// injection script to run with which parameters, and what label the captured
// fixture gets. Off the rig, the same definitions drive the synthetic
// development corpus, through each scenario's `synthetic` hints — so the
// development corpus and the real corpus cover the same ground.
//
// Positives and negative controls are defined together, in one file, on
// purpose: building the positives first invites unconscious tuning against
// them, and the false-correlation rate then surfaces too late to be cheap to
// fix.
package scenarios

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/nathanwebb/tropis/pkg/schema"
)

//go:embed scenarios.yaml
var definitions []byte

// Scenario is one planned capture.
type Scenario struct {
	// ID is the scenario identifier, used as the fixture's scenarioId.
	ID string `json:"id"`

	// Description says what happens in the scenario and why the expected
	// answer is right. It goes into the label's notes.
	Description string `json:"description"`

	// FaultType is the injected fault, as the injection script reports it.
	// Empty for a scenario with no injected fault.
	FaultType string `json:"faultType,omitempty"`

	// Inject is how the fault is injected on the rig.
	Inject *Injection `json:"inject,omitempty"`

	// Workload names the workload manifest under eval/scenarios/workloads.
	Workload string `json:"workload"`

	// Expected is the ground truth.
	Expected Expected `json:"expected"`

	// Hardware lists requirements the rig must meet, such as "real-sata" for
	// scenarios whose SMART changes only real hardware can produce.
	Hardware []string `json:"hardware,omitempty"`

	// Synthetic describes the observable state for the development corpus
	// generator. It is never used on the rig.
	Synthetic SyntheticHints `json:"synthetic"`
}

// Injection names a script under hack/inject and its parameters.
type Injection struct {
	Script string            `json:"script"`
	Params map[string]string `json:"params,omitempty"`
}

// Expected is a scenario's ground truth.
type Expected struct {
	Relationship   schema.Relationship `json:"relationship"`
	RootCauseLayer *schema.Layer       `json:"rootCauseLayer,omitempty"`
}

// SyntheticHints pick the archetypes the development corpus generator uses
// to fabricate this scenario's fixture.
type SyntheticHints struct {
	SMART    string `json:"smart"`
	Workload string `json:"workload"`
	NPD      string `json:"npd,omitempty"`
}

// Known archetypes. Kept here so Validate can reject a typo in the YAML.
var (
	SMARTArchetypes = []string{
		"healthy", "failing", "pending-new", "realloc-growing",
		"realloc-stable", "nvme-media-errors", "unreadable",
	}
	WorkloadArchetypes = []string{
		"io-error-crashloop", "fsync-panic", "probe-timeouts", "readonly-fs",
		"fs-corrupt", "evictions", "config-crash", "oom", "runtime-down",
		"silent-137", "healthy",
	}
	NPDArchetypes = []string{"", "readonly-fs", "containerd-restart", "none-firing"}
)

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Load returns the embedded scenario definitions, validated.
func Load() ([]Scenario, error) {
	return Parse(definitions)
}

// Parse decodes and validates scenario definitions.
func Parse(data []byte) ([]Scenario, error) {
	var doc struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	if err := yaml.UnmarshalStrict(bytes.TrimSpace(data), &doc); err != nil {
		return nil, fmt.Errorf("parse scenarios: %w", err)
	}
	if err := Validate(doc.Scenarios); err != nil {
		return nil, err
	}
	return doc.Scenarios, nil
}

// Validate checks every scenario, reporting all problems.
func Validate(all []Scenario) error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	in := func(v string, set []string) bool {
		for _, s := range set {
			if s == v {
				return true
			}
		}
		return false
	}

	seen := map[string]bool{}
	for i, s := range all {
		where := fmt.Sprintf("scenarios[%d] %q", i, s.ID)
		if !idPattern.MatchString(s.ID) {
			add("%s: id must be lower-case words joined by hyphens", where)
		}
		if seen[s.ID] {
			add("%s: duplicate id", where)
		}
		seen[s.ID] = true
		if strings.TrimSpace(s.Description) == "" {
			add("%s: description is required", where)
		}
		if s.Workload == "" {
			add("%s: workload is required", where)
		}

		// The same invariant verdicts and labels carry.
		label := schema.Label{
			ScenarioID:     s.ID,
			Relationship:   s.Expected.Relationship,
			RootCauseLayer: s.Expected.RootCauseLayer,
			FaultType:      s.FaultType,
		}
		if err := label.Validate(); err != nil {
			add("%s: expected answer is not a valid label: %v", where, err)
		}
		if (s.Inject == nil) != (s.FaultType == "") {
			add("%s: faultType and inject must be set together", where)
		}
		if s.Inject != nil && !strings.HasSuffix(s.Inject.Script, ".sh") {
			add("%s: inject.script must name a script in hack/inject", where)
		}

		if !in(s.Synthetic.SMART, SMARTArchetypes) {
			add("%s: synthetic.smart %q is not one of %v", where, s.Synthetic.SMART, SMARTArchetypes)
		}
		if !in(s.Synthetic.Workload, WorkloadArchetypes) {
			add("%s: synthetic.workload %q is not one of %v", where, s.Synthetic.Workload, WorkloadArchetypes)
		}
		if !in(s.Synthetic.NPD, NPDArchetypes) {
			add("%s: synthetic.npd %q is not one of %v", where, s.Synthetic.NPD, NPDArchetypes)
		}
	}

	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("invalid scenarios:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Label returns the ground-truth label for a scenario.
func (s Scenario) Label() schema.Label {
	return schema.Label{
		ScenarioID:     s.ID,
		Relationship:   s.Expected.Relationship,
		RootCauseLayer: s.Expected.RootCauseLayer,
		FaultType:      s.FaultType,
		Notes:          strings.TrimSpace(s.Description),
	}
}

// Find returns the scenario with the given ID.
func Find(all []Scenario, id string) (Scenario, error) {
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
	}
	return Scenario{}, fmt.Errorf("no scenario %q in scenarios.yaml", id)
}
