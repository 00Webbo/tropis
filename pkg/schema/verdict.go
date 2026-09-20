// Package schema defines the verdict and fixture types that are the source of
// truth for the whole project.
//
// A Verdict is simultaneously three things: the .status of a NodeHealthReport
// custom resource, the output of `tropis analyze --json`, and the input the
// evaluation harness scores. It is defined once here and everything else is
// generated from or serialised through it. If these types and some other
// representation disagree, these types are right.
package schema

//go:generate go run ../../hack/gen-schema -o ../../docs/schema/verdict.schema.json

import (
	"time"
)

// Relationship is the central question Tropis answers: does the host-layer
// evidence explain the Kubernetes-layer symptoms on this node?
//
// Coincidental and InsufficientEvidence are first-class answers, not failure
// modes. A model handed two data streams and asked "is one causing the other"
// is heavily biased toward yes, so an agent that never returns these is not
// working, whatever it scores on positive cases.
type Relationship string

const (
	// RelationshipCausal means the host-layer fault is judged to explain the
	// observed Kubernetes-layer symptoms. Only this value may carry a RootCause.
	RelationshipCausal Relationship = "causal"

	// RelationshipCoincidental means both a host anomaly and a workload problem
	// are present, but the evidence indicates they are unrelated. The classic
	// case is a disk with a stable, long-standing reallocated sector count
	// alongside a pod crashing on a configuration error.
	RelationshipCoincidental Relationship = "coincidental"

	// RelationshipInsufficientEvidence means the available evidence does not
	// support a judgement either way. Preferred over a low-confidence guess.
	RelationshipInsufficientEvidence Relationship = "insufficient_evidence"
)

// Valid reports whether r is a recognised relationship value.
func (r Relationship) Valid() bool {
	switch r {
	case RelationshipCausal, RelationshipCoincidental, RelationshipInsufficientEvidence:
		return true
	}
	return false
}

// Layer identifies which layer of the stack a root cause sits in.
type Layer string

const (
	// LayerHost is the physical and operating-system layer: disks, kernel,
	// firmware, systemd.
	LayerHost Layer = "host"

	// LayerKubernetes is the cluster layer: pod specs, resource limits,
	// scheduling, container images.
	LayerKubernetes Layer = "kubernetes"
)

// Valid reports whether l is a recognised layer value.
func (l Layer) Valid() bool {
	return l == LayerHost || l == LayerKubernetes
}

// Verdict is the complete result of one analysis pass over one node.
//
// Field-level rules enforced by Validate:
//   - Evidence must be non-empty. A conclusion without citations is not a
//     verdict, and the eval harness cannot score one.
//   - RootCause must be nil unless Relationship is causal.
//   - Confidence must be within [0, 1].
type Verdict struct {
	// Node is the Kubernetes node name this verdict concerns.
	Node string `json:"node"`

	// ObservedAt is when the underlying signals were collected, not when the
	// model was called. Fixture replay preserves the original capture time, so
	// this is stable across re-runs of the same fixture.
	ObservedAt time.Time `json:"observedAt"`

	// AgentVersion is the Tropis build that produced this verdict.
	AgentVersion string `json:"agentVersion"`

	// Backend records which model produced this verdict, and under which
	// prompt. Required for reproducibility: an accuracy number is meaningless
	// without knowing what generated it.
	Backend BackendInfo `json:"backend"`

	// Relationship is the central finding. See Relationship.
	Relationship Relationship `json:"relationship"`

	// RootCause is populated only when Relationship is causal, and must be nil
	// otherwise. A coincidental or insufficient-evidence verdict that names a
	// root cause is self-contradictory and is rejected by Validate.
	RootCause *RootCause `json:"rootCause,omitempty"`

	// Confidence is the model's calibrated self-assessment in [0, 1]. The eval
	// harness measures this against actual correctness; it is reported, not
	// trusted.
	Confidence float64 `json:"confidence"`

	// Evidence cites the specific values and log lines the verdict relied on.
	// Required and non-empty, including for insufficient_evidence verdicts —
	// there "here is what I looked at and why it was not enough" is the
	// evidence.
	Evidence []Evidence `json:"evidence"`

	// NextStep is advisory text for a human operator. It is never an
	// instruction to the agent and never triggers an action: Tropis is
	// read-only. Phrase as a recommendation ("schedule for planned drain"),
	// never as a command.
	NextStep string `json:"nextStep,omitempty"`

	// TriggeredBy lists the pre-filter rule IDs that raised this node as a
	// candidate for analysis. Empty for an explicitly requested analysis.
	TriggeredBy []string `json:"triggeredBy,omitempty"`

	// InputDigest is a hash of the exact analysis input, letting a verdict be
	// tied back to the bytes that produced it. Two runs over one fixture must
	// produce the same digest.
	InputDigest string `json:"inputDigest"`
}

// RootCause names the layer and nature of a confirmed causal fault.
type RootCause struct {
	// Layer is where the fault originates.
	Layer Layer `json:"layer"`

	// Description is a human-readable explanation of the fault. Free text: the
	// machine-readable part of a root cause is Layer plus the cited Evidence.
	Description string `json:"description"`
}

// Evidence is a single citation supporting a verdict.
//
// Evidence links a conclusion to the specific observation behind it. This
// exists for two reasons of equal weight: an operator will not trust an
// uncited verdict, and an uncited verdict cannot be scored by the harness.
type Evidence struct {
	// Source is the collector that produced this observation, for example
	// "smartctl", "kubelet-events", or "pod-logs".
	Source string `json:"source"`

	// CollectedAt is when this specific observation was taken.
	CollectedAt time.Time `json:"collectedAt"`

	// Ref locates the observation within its source: a SMART attribute name,
	// an event UID, a log offset.
	Ref string `json:"ref"`

	// Excerpt is the specific line or value relied upon. It has already passed
	// through redaction, since it originates from model-visible input.
	Excerpt string `json:"excerpt"`
}

// BackendInfo identifies the reasoning backend that produced a verdict.
type BackendInfo struct {
	// Provider is the backend family, for example "anthropic", "ollama", or
	// "mock".
	Provider string `json:"provider"`

	// Model is the specific model identifier.
	Model string `json:"model"`

	// PromptVersion is the version of the prompt template used. Changing the
	// prompt changes the results, so eval numbers are only comparable within a
	// prompt version.
	PromptVersion string `json:"promptVersion"`
}
