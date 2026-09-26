package reason

import (
	"context"
	"errors"
	"testing"

	"github.com/nathanwebb/tropis/pkg/schema"
)

// stub is a Backend returning a fixed result and counting calls.
type stub struct {
	v     schema.Verdict
	err   error
	calls int
	name  string
}

func (s *stub) Analyze(context.Context, *AnalysisInput) (schema.Verdict, error) {
	s.calls++
	return s.v, s.err
}
func (s *stub) Describe() schema.BackendInfo {
	return schema.BackendInfo{Provider: "stub", Model: s.name}
}

func verdict(rel schema.Relationship, conf float64, model string) schema.Verdict {
	return schema.Verdict{Relationship: rel, Confidence: conf, Backend: schema.BackendInfo{Model: model}}
}

func TestEscalating(t *testing.T) {
	tests := []struct {
		name      string
		sweep     *stub
		escalate  *stub
		wantModel string
		wantErr   bool
		escalated bool
	}{
		{"confident coincidental stands", &stub{v: verdict(schema.RelationshipCoincidental, 0.85, "sweep")}, &stub{v: verdict(schema.RelationshipCausal, 0.9, "big")}, "sweep", false, false},
		{"causal always escalates", &stub{v: verdict(schema.RelationshipCausal, 0.95, "sweep")}, &stub{v: verdict(schema.RelationshipCausal, 0.9, "big")}, "big", false, true},
		{"low confidence escalates", &stub{v: verdict(schema.RelationshipInsufficientEvidence, 0.4, "sweep")}, &stub{v: verdict(schema.RelationshipCoincidental, 0.8, "big")}, "big", false, true},
		{"malformed sweep escalates", &stub{err: ErrMalformedOutput}, &stub{v: verdict(schema.RelationshipCoincidental, 0.8, "big")}, "big", false, true},
		{"sweep survives failed escalation", &stub{v: verdict(schema.RelationshipCausal, 0.9, "sweep")}, &stub{err: errors.New("503")}, "sweep", false, true},
		{"both fail", &stub{err: ErrMalformedOutput}, &stub{err: errors.New("503")}, "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Escalating{Sweep: tt.sweep, Escalate: tt.escalate}
			v, err := e.Analyze(context.Background(), &AnalysisInput{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if v.Backend.Model != tt.wantModel {
				t.Errorf("verdict from %q, want %q", v.Backend.Model, tt.wantModel)
			}
			if (tt.escalate.calls > 0) != tt.escalated {
				t.Errorf("escalation calls = %d", tt.escalate.calls)
			}
		})
	}
}
