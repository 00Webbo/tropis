package schema

import (
	"errors"
	"fmt"
	"strings"
)

// ValidationError describes one failed constraint on a Verdict.
type ValidationError struct {
	// Field is the JSON path of the offending field, for example "evidence[0].source".
	Field string
	// Msg explains what was wrong with it.
	Msg string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

// ValidationErrors is the set of constraints a Verdict failed.
//
// Validate reports every violation rather than stopping at the first, because
// the common caller is a model backend parsing generated output, and one
// round trip listing all the problems beats several listing one each.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "no validation errors"
	}
	parts := make([]string, 0, len(e))
	for _, v := range e {
		parts = append(parts, v.Error())
	}
	return "invalid verdict: " + strings.Join(parts, "; ")
}

// Is lets errors.Is(err, ErrInvalidVerdict) match a ValidationErrors value.
func (e ValidationErrors) Is(target error) bool { return target == ErrInvalidVerdict }

// ErrInvalidVerdict is a sentinel matching any validation failure, so callers
// can test errors.Is(err, schema.ErrInvalidVerdict) without unwrapping the
// individual violations.
var ErrInvalidVerdict = errors.New("invalid verdict")

// Validate checks a Verdict against every structural constraint the project
// relies on, returning a ValidationErrors listing all violations, or nil.
//
// This is the single enforcement point. Model backends must run it on parsed
// output: a malformed generation has to become an error, never a fabricated
// verdict that reaches a CRD or an eval score.
func (v *Verdict) Validate() error {
	var errs ValidationErrors

	add := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Msg: msg})
	}

	if strings.TrimSpace(v.Node) == "" {
		add("node", "must not be empty")
	}
	if v.ObservedAt.IsZero() {
		add("observedAt", "must be set")
	}
	if strings.TrimSpace(v.InputDigest) == "" {
		add("inputDigest", "must be set, so the verdict can be tied to its input")
	}

	if !v.Relationship.Valid() {
		add("relationship", fmt.Sprintf(
			"must be one of %q, %q, %q; got %q",
			RelationshipCausal, RelationshipCoincidental,
			RelationshipInsufficientEvidence, v.Relationship))
	}

	// A root cause is a claim only a causal verdict is entitled to make.
	switch {
	case v.Relationship == RelationshipCausal && v.RootCause == nil:
		add("rootCause", "must be set when relationship is causal")
	case v.Relationship != RelationshipCausal && v.RootCause != nil:
		add("rootCause", fmt.Sprintf(
			"must be nil unless relationship is %q; got relationship %q",
			RelationshipCausal, v.Relationship))
	}

	if v.RootCause != nil {
		if !v.RootCause.Layer.Valid() {
			add("rootCause.layer", fmt.Sprintf(
				"must be %q or %q; got %q", LayerHost, LayerKubernetes, v.RootCause.Layer))
		}
		if strings.TrimSpace(v.RootCause.Description) == "" {
			add("rootCause.description", "must not be empty")
		}
	}

	if v.Confidence < 0 || v.Confidence > 1 {
		add("confidence", fmt.Sprintf("must be within [0, 1]; got %v", v.Confidence))
	}

	// Evidence is required for every relationship, including
	// insufficient_evidence: what was examined and found wanting is itself the
	// evidence for that conclusion.
	if len(v.Evidence) == 0 {
		add("evidence", "must not be empty; a verdict must cite what it relied on")
	}
	for i, ev := range v.Evidence {
		if strings.TrimSpace(ev.Source) == "" {
			add(fmt.Sprintf("evidence[%d].source", i), "must not be empty")
		}
		if strings.TrimSpace(ev.Excerpt) == "" {
			add(fmt.Sprintf("evidence[%d].excerpt", i),
				"must not be empty; cite the specific value or line relied on")
		}
		if ev.CollectedAt.IsZero() {
			add(fmt.Sprintf("evidence[%d].collectedAt", i), "must be set")
		}
	}

	if strings.TrimSpace(v.Backend.Provider) == "" {
		add("backend.provider", "must be set")
	}
	if strings.TrimSpace(v.Backend.Model) == "" {
		add("backend.model", "must be set")
	}
	if strings.TrimSpace(v.Backend.PromptVersion) == "" {
		add("backend.promptVersion",
			"must be set; results are only comparable within a prompt version")
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}
