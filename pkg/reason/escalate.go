package reason

import (
	"context"
	"errors"

	"github.com/nathanwebb/tropis/pkg/schema"
)

// Escalating runs a cheaper sweep backend first and escalates to a stronger
// one when the answer matters or is shaky.
//
// It escalates when the sweep claims causality — the verdict an operator is
// most likely to act on, and the one a model is most biased toward — when the
// sweep's confidence is below Below, or when the sweep's output was
// malformed. Otherwise the sweep's verdict stands.
//
// It is provider-neutral: any two Backends compose, local or hosted.
type Escalating struct {
	Sweep    Backend
	Escalate Backend
	// Below is the sweep confidence under which the escalation backend is
	// consulted. Zero means 0.7.
	Below float64
}

// Describe reports the escalation backend, whose verdict is the one that
// counts when there is doubt. Each verdict records the backend that actually
// produced it.
func (e *Escalating) Describe() schema.BackendInfo { return e.Escalate.Describe() }

// Analyze runs the sweep, then escalates if warranted.
func (e *Escalating) Analyze(ctx context.Context, in *AnalysisInput) (schema.Verdict, error) {
	below := e.Below
	if below == 0 {
		below = 0.7
	}

	v, err := e.Sweep.Analyze(ctx, in)
	switch {
	case errors.Is(err, ErrUnbuiltInput), ctx.Err() != nil:
		return schema.Verdict{}, err
	case err == nil && v.Relationship != schema.RelationshipCausal && v.Confidence >= below:
		return v, nil
	}

	escalated, escErr := e.Escalate.Analyze(ctx, in)
	if escErr != nil {
		// A sound sweep verdict survives a failed escalation.
		if err == nil {
			return v, nil
		}
		return schema.Verdict{}, escErr
	}
	return escalated, nil
}
