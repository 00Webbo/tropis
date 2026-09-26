package runner

import (
	"strings"
	"testing"

	"github.com/nathanwebb/tropis/pkg/schema"
)

func layer(l schema.Layer) *schema.Layer { return &l }

func verdict(rel schema.Relationship, l schema.Layer, conf float64) *schema.Verdict {
	v := &schema.Verdict{Relationship: rel, Confidence: conf}
	if rel == schema.RelationshipCausal {
		v.RootCause = &schema.RootCause{Layer: l, Description: "x"}
	}
	return v
}

func scored(v *schema.Verdict, exp schema.Relationship, l *schema.Layer, variant string) CaseResult {
	label := &schema.Label{Relationship: exp, RootCauseLayer: l}
	c := CaseResult{Verdict: v, Variant: variant, Expected: Expected{Relationship: exp, RootCauseLayer: l}, Score: score(v, label)}
	if v == nil {
		c.ErrorKind = ErrKindMalformed
	}
	return c
}

func TestScore(t *testing.T) {
	host := layer(schema.LayerHost)
	tests := []struct {
		name                         string
		v                            *schema.Verdict
		exp                          schema.Relationship
		l                            *schema.Layer
		correct, relCorrect, falseCo bool
	}{
		{"right cause, right layer", verdict("causal", "host", 0.9), "causal", host, true, true, false},
		{"causal but wrong layer", verdict("causal", "kubernetes", 0.9), "causal", host, false, true, false},
		{"missed a causal", verdict("coincidental", "", 0.6), "causal", host, false, false, false},
		{"false correlation", verdict("causal", "host", 0.9), "coincidental", nil, false, false, true},
		{"causal claim on insufficient", verdict("causal", "host", 0.9), "insufficient_evidence", nil, false, false, true},
		{"correct coincidental", verdict("coincidental", "", 0.7), "coincidental", nil, true, true, false},
		{"no verdict", nil, "causal", host, false, false, false},
	}
	for _, tt := range tests {
		s := score(tt.v, &schema.Label{Relationship: tt.exp, RootCauseLayer: tt.l})
		if s.Correct != tt.correct || s.RelationshipCorrect != tt.relCorrect || s.FalseCorrelation != tt.falseCo {
			t.Errorf("%s: %+v", tt.name, s)
		}
	}
}

func TestComputeMetrics(t *testing.T) {
	host := layer(schema.LayerHost)
	cases := []CaseResult{
		scored(verdict("causal", "host", 0.9), "causal", host, "npd-absent"),
		scored(verdict("causal", "host", 0.9), "causal", host, "npd-absent"),
		scored(verdict("coincidental", "", 0.3), "causal", host, "npd-absent"),
		scored(nil, "causal", host, "npd-absent"),
		scored(verdict("causal", "host", 0.8), "coincidental", nil, "npd-absent"),
		scored(verdict("coincidental", "", 0.7), "coincidental", nil, "npd-absent"),
		scored(verdict("insufficient_evidence", "", 0.5), "insufficient_evidence", nil, "npd-absent"),
	}
	m := computeMetrics(cases)
	if m.Positives != 4 || m.RootCauseCorrect != 2 || m.RootCauseAccuracy != 0.5 {
		t.Errorf("positives %d correct %d accuracy %v", m.Positives, m.RootCauseCorrect, m.RootCauseAccuracy)
	}
	if m.NegativeControls != 2 || m.FalseCorrelations != 1 || m.FalseCorrelationRate != 0.5 {
		t.Errorf("negatives %d false %d rate %v", m.NegativeControls, m.FalseCorrelations, m.FalseCorrelationRate)
	}
	if m.Confusion["causal"]["error"] != 1 || m.Confusion["coincidental"]["causal"] != 1 {
		t.Errorf("confusion = %v", m.Confusion)
	}
	if m.Errors[ErrKindMalformed] != 1 {
		t.Errorf("errors = %v", m.Errors)
	}

	// Calibration: six verdicts; the 0.9 bin has two, both correct.
	var ninety CalibrationBin
	for _, b := range m.Calibration.Bins {
		if b.Lower == 0.9 {
			ninety = b
		}
	}
	if ninety.Count != 2 || ninety.Accuracy != 1 {
		t.Errorf("0.9 bin = %+v", ninety)
	}
	if m.Calibration.Brier <= 0 || m.Calibration.ECE <= 0 {
		t.Errorf("calibration = %+v", m.Calibration)
	}
}

// A perfectly calibrated set scores zero Brier and zero ECE.
func TestCalibrationPerfect(t *testing.T) {
	host := layer(schema.LayerHost)
	var cases []CaseResult
	for i := 0; i < 5; i++ {
		cases = append(cases, scored(verdict("causal", "host", 1.0), "causal", host, "npd-absent"))
		cases = append(cases, scored(verdict("causal", "host", 0.0), "coincidental", nil, "npd-absent"))
	}
	m := computeMetrics(cases)
	if m.Calibration.Brier != 0 || m.Calibration.ECE != 0 {
		t.Errorf("calibration = %+v", m.Calibration)
	}
}

func TestGate(t *testing.T) {
	mk := func(synthetic bool, pos, correct, neg, falseCo int) *Results {
		r := &Results{Synthetic: synthetic}
		r.Metrics = Metrics{Positives: pos, RootCauseCorrect: correct, RootCauseAccuracy: ratio(correct, pos),
			NegativeControls: neg, FalseCorrelations: falseCo, FalseCorrelationRate: ratio(falseCo, neg)}
		return r
	}
	tests := []struct {
		name string
		r    *Results
		want string
	}{
		{"synthetic never passes", mk(true, 40, 40, 12, 0), "not_applicable"},
		{"too few positives", mk(false, 19, 19, 10, 0), "not_applicable"},
		{"no negative controls", mk(false, 20, 20, 0, 0), "not_applicable"},
		{"passes at the bar", mk(false, 20, 16, 10, 0), "pass"},
		{"fails on accuracy", mk(false, 20, 15, 10, 0), "fail"},
		{"fails at exactly 10% false correlation", mk(false, 20, 20, 10, 1), "fail"},
	}
	for _, tt := range tests {
		if g := evaluateGate(tt.r); g.Status != tt.want {
			t.Errorf("%s: %+v", tt.name, g)
		}
	}
}

func TestWarningsForMissingVariant(t *testing.T) {
	r := &Results{Cases: []CaseResult{{ScenarioID: "a", Variant: "npd-absent"}}}
	r.Metrics = computeMetrics(r.Cases)
	if !strings.Contains(strings.Join(warnings(r), " "), "NPD variants") {
		t.Errorf("warnings = %v", warnings(r))
	}
}
