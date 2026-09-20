package schema

import (
	"fmt"
	"strings"
)

// Validate checks a Fixture for the structural properties the harness relies
// on, returning a ValidationErrors listing every violation, or nil.
func (f *Fixture) Validate() error {
	var errs ValidationErrors
	add := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Msg: msg})
	}

	if strings.TrimSpace(f.Version) == "" {
		add("version", "must be set")
	}
	if strings.TrimSpace(f.ScenarioID) == "" {
		add("scenarioId", "must be set")
	}
	if strings.TrimSpace(f.Node) == "" {
		add("node", "must be set")
	}
	if f.CapturedAt.IsZero() {
		add("capturedAt", "must be set")
	}

	if !f.Variant.Valid() {
		add("variant", fmt.Sprintf("must be %q or %q; got %q",
			VariantNPDPresent, VariantNPDAbsent, f.Variant))
	}

	// A fixture claiming one thing in its variant and another in its
	// environment would silently corrupt the NPD-dependence measurement, which
	// is one of the numbers the project publishes.
	if f.Variant.Valid() {
		wantNPD := f.Variant == VariantNPDPresent
		if f.Environment.NPDPresent != wantNPD {
			add("environment.npdPresent", fmt.Sprintf(
				"is %v but variant is %q; they must agree",
				f.Environment.NPDPresent, f.Variant))
		}
	}

	if strings.TrimSpace(f.Environment.Kernel) == "" {
		add("environment.kernel", "must be set; a capture is not interpretable without it")
	}
	if strings.TrimSpace(f.Environment.KubernetesVersion) == "" {
		add("environment.kubernetesVersion", "must be set")
	}

	// The host capture is the point of the fixture. v1 is SMART only, so a
	// fixture with no SMART data has captured nothing.
	if len(f.Host.SMART) == 0 {
		add("host.smart", "must contain at least one device capture")
	}
	for dev, raw := range f.Host.SMART {
		field := fmt.Sprintf("host.smart[%s]", dev)
		if raw.IsEmpty() {
			add(field, "must not be empty")
			continue
		}
		if !raw.Valid() {
			add(field, "must be valid JSON as emitted by `smartctl -j`")
		}
	}
	if f.Host.CollectedAt.IsZero() {
		add("host.collectedAt", "must be set")
	}

	for i, p := range f.Kubernetes.Pods {
		if !p.IsEmpty() && !p.Valid() {
			add(fmt.Sprintf("kubernetes.pods[%d]", i), "must be valid JSON")
		}
	}
	for i, e := range f.Kubernetes.Events {
		if !e.IsEmpty() && !e.Valid() {
			add(fmt.Sprintf("kubernetes.events[%d]", i), "must be valid JSON")
		}
	}
	if !f.Kubernetes.NodeJSON.IsEmpty() && !f.Kubernetes.NodeJSON.Valid() {
		add("kubernetes.nodeJson", "must be valid JSON")
	}

	// NPD conditions in an npd-absent capture means the capture is mislabelled.
	if f.Variant == VariantNPDAbsent && len(f.Kubernetes.NPDConditions) > 0 {
		add("kubernetes.npdConditions", fmt.Sprintf(
			"must be empty in a %q capture", VariantNPDAbsent))
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Validate checks a Label for internal consistency.
func (l *Label) Validate() error {
	var errs ValidationErrors
	add := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Msg: msg})
	}

	if strings.TrimSpace(l.ScenarioID) == "" {
		add("scenarioId", "must be set")
	}
	if !l.Relationship.Valid() {
		add("relationship", fmt.Sprintf(
			"must be one of %q, %q, %q; got %q",
			RelationshipCausal, RelationshipCoincidental,
			RelationshipInsufficientEvidence, l.Relationship))
	}

	// The same invariant the Verdict carries: a root cause is a claim only a
	// causal scenario is entitled to make. Ground truth that violates it would
	// score verdicts against an impossible target.
	switch {
	case l.Relationship == RelationshipCausal && l.RootCauseLayer == nil:
		add("rootCauseLayer", "must be set when relationship is causal")
	case l.Relationship != RelationshipCausal && l.RootCauseLayer != nil:
		add("rootCauseLayer", fmt.Sprintf(
			"must be nil unless relationship is %q; got %q",
			RelationshipCausal, l.Relationship))
	}

	if l.RootCauseLayer != nil && !l.RootCauseLayer.Valid() {
		add("rootCauseLayer", fmt.Sprintf("must be %q or %q; got %q",
			LayerHost, LayerKubernetes, *l.RootCauseLayer))
	}

	// A causal scenario was caused by something, and that something is what
	// the injection scripts emit.
	if l.Relationship == RelationshipCausal && strings.TrimSpace(l.FaultType) == "" {
		add("faultType", "must be set for a causal scenario")
	}

	if l.Injection != nil {
		if strings.TrimSpace(l.Injection.Tool) == "" {
			add("injection.tool", "must be set")
		}
		if l.Injection.StartedAt.IsZero() {
			add("injection.startedAt", "must be set")
		}
		if !l.Injection.EndedAt.IsZero() && l.Injection.EndedAt.Before(l.Injection.StartedAt) {
			add("injection.endedAt", "must not precede startedAt")
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Matches reports whether a label belongs to a fixture. The scorer checks this
// before scoring, since a label paired with the wrong fixture would produce a
// confidently wrong accuracy number.
func (l *Label) Matches(f *Fixture) error {
	if l.ScenarioID != f.ScenarioID {
		return fmt.Errorf("label scenarioId %q does not match fixture scenarioId %q",
			l.ScenarioID, f.ScenarioID)
	}
	return nil
}
