package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// validVerdict returns a verdict passing every constraint, for tests to break
// one field at a time.
func validVerdict() Verdict {
	ts := time.Date(2026, 9, 20, 14, 30, 0, 0, time.UTC)
	return Verdict{
		Node:         "worker-03",
		ObservedAt:   ts,
		AgentVersion: "v0.1.0",
		Backend: BackendInfo{
			Provider:      "mock",
			Model:         "mock-1",
			PromptVersion: "2026-09-20.1",
		},
		Relationship: RelationshipCausal,
		RootCause: &RootCause{
			Layer:       LayerHost,
			Description: "Disk /dev/sda is actively remapping sectors under write load.",
		},
		Confidence: 0.87,
		Evidence: []Evidence{{
			Source:      "smartctl",
			CollectedAt: ts,
			Ref:         "attribute/5/Reallocated_Sector_Ct",
			Excerpt:     "Reallocated_Sector_Ct 488 (was 312 6h ago)",
		}},
		NextStep:    "Schedule worker-03 for planned drain and disk replacement.",
		TriggeredBy: []string{"smart.reallocated_growth"},
		InputDigest: "sha256:0b5c1e",
	}
}

func TestVerdictRoundTrip(t *testing.T) {
	original := validVerdict()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Verdict
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(original, got) {
		t.Errorf("round trip changed the verdict:\n before: %+v\n  after: %+v", original, got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("round-tripped verdict no longer validates: %v", err)
	}
}

// Backend settings that change results must survive a round trip, and must
// not appear at all when unset, so verdicts from before they existed and
// verdicts that set none serialise identically.
func TestBackendSettingsRoundTrip(t *testing.T) {
	v := validVerdict()
	data, err := json.Marshal(v.Backend)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "effort") || strings.Contains(string(data), "think") {
		t.Errorf("unset settings serialised: %s", data)
	}

	v.Backend.Effort = "high"
	v.Backend.Think = "false"
	data, err = json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got Verdict
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Backend != v.Backend {
		t.Errorf("backend = %+v, want %+v", got.Backend, v.Backend)
	}
}

// A nil RootCause must not serialise as a JSON null, since the CRD status and
// the --json output are the same bytes and consumers will index into it.
func TestVerdictOmitsNilRootCause(t *testing.T) {
	v := validVerdict()
	v.Relationship = RelationshipInsufficientEvidence
	v.RootCause = nil

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "rootCause") {
		t.Errorf("nil rootCause should be omitted, got: %s", data)
	}
}

func TestVerdictValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Verdict)
		wantValid bool
		wantField string
	}{
		{
			name:      "valid causal verdict",
			mutate:    func(*Verdict) {},
			wantValid: true,
		},
		{
			name: "valid coincidental verdict without root cause",
			mutate: func(v *Verdict) {
				v.Relationship = RelationshipCoincidental
				v.RootCause = nil
			},
			wantValid: true,
		},
		{
			name: "valid insufficient_evidence verdict still cites evidence",
			mutate: func(v *Verdict) {
				v.Relationship = RelationshipInsufficientEvidence
				v.RootCause = nil
				v.Confidence = 0.2
			},
			wantValid: true,
		},
		{
			name:      "empty evidence is rejected",
			mutate:    func(v *Verdict) { v.Evidence = nil },
			wantField: "evidence",
		},
		{
			name: "root cause on a coincidental verdict is rejected",
			mutate: func(v *Verdict) {
				v.Relationship = RelationshipCoincidental
				// RootCause deliberately left populated.
			},
			wantField: "rootCause",
		},
		{
			name: "root cause on an insufficient_evidence verdict is rejected",
			mutate: func(v *Verdict) {
				v.Relationship = RelationshipInsufficientEvidence
			},
			wantField: "rootCause",
		},
		{
			name: "causal verdict without a root cause is rejected",
			mutate: func(v *Verdict) {
				v.RootCause = nil
			},
			wantField: "rootCause",
		},
		{
			name:      "unknown relationship is rejected",
			mutate:    func(v *Verdict) { v.Relationship = "probably" },
			wantField: "relationship",
		},
		{
			name:      "empty relationship is rejected",
			mutate:    func(v *Verdict) { v.Relationship = "" },
			wantField: "relationship",
		},
		{
			name:      "confidence above 1 is rejected",
			mutate:    func(v *Verdict) { v.Confidence = 1.5 },
			wantField: "confidence",
		},
		{
			name:      "negative confidence is rejected",
			mutate:    func(v *Verdict) { v.Confidence = -0.1 },
			wantField: "confidence",
		},
		{
			name:      "confidence of exactly 0 is allowed",
			mutate:    func(v *Verdict) { v.Confidence = 0 },
			wantValid: true,
		},
		{
			name:      "confidence of exactly 1 is allowed",
			mutate:    func(v *Verdict) { v.Confidence = 1 },
			wantValid: true,
		},
		{
			name:      "empty node is rejected",
			mutate:    func(v *Verdict) { v.Node = "  " },
			wantField: "node",
		},
		{
			name:      "missing input digest is rejected",
			mutate:    func(v *Verdict) { v.InputDigest = "" },
			wantField: "inputDigest",
		},
		{
			name:      "zero observedAt is rejected",
			mutate:    func(v *Verdict) { v.ObservedAt = time.Time{} },
			wantField: "observedAt",
		},
		{
			name:      "unknown root cause layer is rejected",
			mutate:    func(v *Verdict) { v.RootCause.Layer = "firmware" },
			wantField: "rootCause.layer",
		},
		{
			name:      "empty root cause description is rejected",
			mutate:    func(v *Verdict) { v.RootCause.Description = "" },
			wantField: "rootCause.description",
		},
		{
			name:      "evidence without a source is rejected",
			mutate:    func(v *Verdict) { v.Evidence[0].Source = "" },
			wantField: "evidence[0].source",
		},
		{
			name:      "evidence without an excerpt is rejected",
			mutate:    func(v *Verdict) { v.Evidence[0].Excerpt = "" },
			wantField: "evidence[0].excerpt",
		},
		{
			name:      "evidence without a timestamp is rejected",
			mutate:    func(v *Verdict) { v.Evidence[0].CollectedAt = time.Time{} },
			wantField: "evidence[0].collectedAt",
		},
		{
			name:      "missing prompt version is rejected",
			mutate:    func(v *Verdict) { v.Backend.PromptVersion = "" },
			wantField: "backend.promptVersion",
		},
		{
			name:      "missing backend model is rejected",
			mutate:    func(v *Verdict) { v.Backend.Model = "" },
			wantField: "backend.model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validVerdict()
			tt.mutate(&v)

			err := v.Validate()

			if tt.wantValid {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			if !errors.Is(err, ErrInvalidVerdict) {
				t.Errorf("error should match ErrInvalidVerdict, got %T: %v", err, err)
			}

			var verrs ValidationErrors
			if !errors.As(err, &verrs) {
				t.Fatalf("error should be a ValidationErrors, got %T", err)
			}
			var found bool
			for _, ve := range verrs {
				if ve.Field == tt.wantField {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a violation on field %q, got: %v", tt.wantField, err)
			}
		})
	}
}

// Validate reports every violation at once, so a backend re-prompting on
// malformed output can fix them in one pass.
func TestValidateReportsAllViolations(t *testing.T) {
	v := validVerdict()
	v.Node = ""
	v.Evidence = nil
	v.Confidence = 9

	err := v.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if len(verrs) < 3 {
		t.Errorf("expected at least 3 violations, got %d: %v", len(verrs), verrs)
	}
}

func TestRelationshipValid(t *testing.T) {
	valid := []Relationship{
		RelationshipCausal, RelationshipCoincidental, RelationshipInsufficientEvidence,
	}
	for _, r := range valid {
		if !r.Valid() {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range []Relationship{"", "unrelated", "CAUSAL", "yes"} {
		if r.Valid() {
			t.Errorf("%q should not be valid", r)
		}
	}
}

// The wire values are an API commitment: the CRD, the JSON output and the
// eval labels all depend on these exact strings.
func TestRelationshipWireValues(t *testing.T) {
	want := map[Relationship]string{
		RelationshipCausal:               "causal",
		RelationshipCoincidental:         "coincidental",
		RelationshipInsufficientEvidence: "insufficient_evidence",
	}
	for r, s := range want {
		if string(r) != s {
			t.Errorf("relationship wire value changed: got %q, want %q", r, s)
		}
	}
	if string(LayerHost) != "host" || string(LayerKubernetes) != "kubernetes" {
		t.Errorf("layer wire values changed: %q, %q", LayerHost, LayerKubernetes)
	}
}
