// Package notify tells people when a node's verdict changes.
//
// Notifications fire on changes, never on every sweep: a sweep runs every
// fifteen minutes, and a channel that repeats the same verdict each time is a
// channel people mute. What counts as a change worth sending is decided by a
// Policy.
//
// Everything a notification carries comes from a verdict, and every
// excerpt in a verdict comes from redacted model input, so notifications
// carry nothing the reasoning layer was not already allowed to see. They do
// leave the cluster, to whatever endpoint is configured; SECURITY.md says so.
package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/00Webbo/tropis/pkg/schema"
)

// Kind classifies a change.
type Kind string

const (
	// KindNew is the first verdict for a node.
	KindNew Kind = "new"
	// KindChanged is a verdict whose relationship or root-cause layer
	// differs from the previous one.
	KindChanged Kind = "changed"
	// KindCleared is a node whose previous verdict was causal and whose
	// current one is not: the fault has gone, or the evidence for it has.
	KindCleared Kind = "cleared"
)

// Change is a notable transition in one node's verdict.
type Change struct {
	Kind     Kind            `json:"change"`
	Node     string          `json:"node"`
	Previous *schema.Verdict `json:"previous"`
	Current  schema.Verdict  `json:"verdict"`
}

// Notifier delivers a change somewhere.
type Notifier interface {
	Notify(ctx context.Context, c Change) error
	// Name identifies the notifier in errors and logs.
	Name() string
}

// Policy decides which transitions are worth a notification.
type Policy string

const (
	// PolicyCausal (the default) notifies when a node becomes causal, when a
	// causal node's root-cause layer changes, and when it stops being causal.
	// Coincidental and insufficient-evidence verdicts on their own are quiet:
	// they are findings, not calls to act.
	PolicyCausal Policy = "causal"
	// PolicyAny notifies on any change of relationship or root-cause layer,
	// including a node's first verdict whatever it is.
	PolicyAny Policy = "any"
)

// ParsePolicy reads a policy name; empty means PolicyCausal.
func ParsePolicy(s string) (Policy, error) {
	switch Policy(s) {
	case "", PolicyCausal:
		return PolicyCausal, nil
	case PolicyAny:
		return PolicyAny, nil
	}
	return "", fmt.Errorf("unknown notification policy %q (want %q or %q)", s, PolicyCausal, PolicyAny)
}

// Evaluate returns the change between two verdicts, or nil when there is
// nothing worth sending.
//
// Only the relationship and the root-cause layer are compared. The
// description, confidence and evidence are model-written and vary from run
// to run on an unchanged node; comparing them would notify every sweep.
func (p Policy) Evaluate(prev *schema.Verdict, cur schema.Verdict) *Change {
	causal := func(v *schema.Verdict) bool { return v != nil && v.Relationship == schema.RelationshipCausal }
	layer := func(v *schema.Verdict) schema.Layer {
		if v == nil || v.RootCause == nil {
			return ""
		}
		return v.RootCause.Layer
	}
	c := &Change{Node: cur.Node, Previous: prev, Current: cur}

	switch {
	case prev == nil:
		if p == PolicyAny || causal(&cur) {
			c.Kind = KindNew
			return c
		}
	case causal(prev) && !causal(&cur):
		c.Kind = KindCleared
		return c
	case prev.Relationship != cur.Relationship || layer(prev) != layer(&cur):
		if p == PolicyAny || causal(&cur) {
			c.Kind = KindChanged
			return c
		}
	}
	return nil
}

// Multi sends to several notifiers, attempting every one and reporting
// every failure: one broken webhook must not silence the others.
type Multi []Notifier

// Notify delivers to each notifier in turn.
func (m Multi) Notify(ctx context.Context, c Change) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, c); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Name identifies the set.
func (m Multi) Name() string { return "notifiers" }

// Headline is a one-line summary of a change, shared by every notifier so
// they all say the same thing.
func Headline(c Change) string {
	v := c.Current
	switch c.Kind {
	case KindCleared:
		return fmt.Sprintf("%s is no longer causal: now %s (confidence %.2f)", v.Node, v.Relationship, v.Confidence)
	default:
		if v.RootCause != nil {
			return fmt.Sprintf("%s: %s, %s layer (confidence %.2f)", v.Node, v.Relationship, v.RootCause.Layer, v.Confidence)
		}
		return fmt.Sprintf("%s: %s (confidence %.2f)", v.Node, v.Relationship, v.Confidence)
	}
}
