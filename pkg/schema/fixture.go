package schema

import (
	"time"
)

// FixtureVersion is the current fixture format version. Fixtures record the
// version they were captured under so the corpus survives format changes
// without a recapture, which matters because recapture needs hardware.
const FixtureVersion = "v1alpha1"

// Fixture is one captured scenario: everything the collectors saw on one node
// at one moment, stored so the whole pipeline can be replayed offline.
//
// This type deliberately does not contain the ground-truth label. The label
// lives in a separate Label type, in a separate file on disk, loaded by a
// separate function. That separation is the reason the eval harness can claim
// to be blind — see the Label doc comment.
type Fixture struct {
	// Version is the fixture format version, for forward compatibility.
	Version string `json:"version"`

	// ScenarioID identifies the scenario this capture belongs to, for example
	// "disk-realloc-growth-01". It joins a Fixture to its Label and to the
	// scenario definition.
	ScenarioID string `json:"scenarioId"`

	// Variant distinguishes captures of the same scenario taken under
	// different conditions — principally NPD present versus absent, since the
	// accuracy numbers must show whether they depend on NPD.
	Variant Variant `json:"variant"`

	// CapturedAt is when this capture was taken.
	CapturedAt time.Time `json:"capturedAt"`

	// Node is the node name the capture came from.
	Node string `json:"node"`

	// Environment describes the rig this was captured on. Recorded per fixture
	// rather than per corpus because a corpus accumulates across rebuilds of
	// the rig, and "which kernel was this?" is unanswerable afterwards.
	Environment Environment `json:"environment"`

	// Host is the raw host-layer capture.
	Host HostCapture `json:"host"`

	// Kubernetes is the raw Kubernetes-layer capture.
	Kubernetes K8sCapture `json:"kubernetes"`

	// Baseline is what the incumbent tooling concluded about this same
	// scenario. Uplift over the incumbent is the claim that matters, and
	// capturing it later would mean re-running every injection.
	Baseline Baseline `json:"baseline"`

	// Synthetic marks a fixture as fabricated for development rather than
	// captured from real hardware. Synthetic fixtures must never be published
	// as evaluation results; the runner refuses to include them in a
	// publishable report.
	Synthetic bool `json:"synthetic,omitempty"`
}

// Variant identifies a capture condition for a scenario.
type Variant string

const (
	// VariantNPDPresent is a capture taken with node-problem-detector running.
	VariantNPDPresent Variant = "npd-present"

	// VariantNPDAbsent is a capture taken with no NPD installed. Every
	// scenario is captured in both variants.
	VariantNPDAbsent Variant = "npd-absent"
)

// Valid reports whether v is a recognised variant.
func (v Variant) Valid() bool {
	return v == VariantNPDPresent || v == VariantNPDAbsent
}

// Environment records the rig a fixture was captured on.
type Environment struct {
	// Kernel is the kernel release string, as `uname -r`.
	Kernel string `json:"kernel"`

	// OSRelease is the distribution identifier, for example "Ubuntu 24.04.1 LTS".
	OSRelease string `json:"osRelease,omitempty"`

	// KubernetesVersion is the server version, for example "v1.31.2".
	KubernetesVersion string `json:"kubernetesVersion"`

	// ContainerRuntime is the runtime and version, for example "containerd://1.7.22".
	ContainerRuntime string `json:"containerRuntime,omitempty"`

	// DiskModel is the model of the device under test.
	DiskModel string `json:"diskModel"`

	// DiskTransport is "sata", "nvme" or "sas". Attribute semantics differ
	// between them, so a fixture is not interpretable without it.
	DiskTransport string `json:"diskTransport,omitempty"`

	// NPDPresent records whether node-problem-detector was installed. It must
	// agree with Variant; the loader checks this, since a mismatch would
	// silently corrupt the NPD-dependence measurement.
	NPDPresent bool `json:"npdPresent"`

	// NPDVersion is the NPD version, when present.
	NPDVersion string `json:"npdVersion,omitempty"`
}

// HostCapture is the raw host-layer signal for one node.
//
// v1 is SMART only. Fields for other signals are deliberately absent rather
// than present and empty: an empty field invites a collector to start filling
// it before its scenarios exist.
type HostCapture struct {
	// SMART holds the raw `smartctl -j` output per device, keyed by device
	// path (for example "/dev/sda").
	//
	// Stored as raw JSON rather than parsed, so the parser itself is exercised
	// on replay. A fixture that stored parsed output could not catch a parser
	// regression, which is half of what the corpus is for.
	SMART map[string]RawJSON `json:"smart"`

	// CollectedAt is when the host capture was taken.
	CollectedAt time.Time `json:"collectedAt"`

	// Previous is an earlier capture of the same devices, when one exists.
	//
	// A single snapshot cannot distinguish a drive that has carried twelve
	// reallocated sectors for a year from one that gained twelve this
	// morning, and that distinction is exactly the line between a
	// coincidental and a causal verdict. The collector keeps an hourly
	// history and supplies the oldest reading it holds, up to a day back.
	Previous *HostSnapshot `json:"previous,omitempty"`
}

// HostSnapshot is an earlier raw SMART capture.
type HostSnapshot struct {
	SMART       map[string]RawJSON `json:"smart"`
	CollectedAt time.Time          `json:"collectedAt"`
}

// K8sCapture is the raw Kubernetes-layer state for one node.
type K8sCapture struct {
	// NodeJSON is the full Node object as returned by the API server.
	NodeJSON RawJSON `json:"nodeJson,omitempty"`

	// Pods holds the full Pod objects scheduled to this node.
	Pods []RawJSON `json:"pods,omitempty"`

	// Events holds Event objects relating to this node and its pods.
	Events []RawJSON `json:"events,omitempty"`

	// Logs holds container log excerpts, keyed by "namespace/pod/container".
	// Previous-container logs use a "/previous" suffix on the key.
	Logs map[string]string `json:"logs,omitempty"`

	// NPDConditions holds node conditions reported by node-problem-detector,
	// when present.
	//
	// These may feed the pre-filter as an extra trigger. They are never
	// evidence for the reasoning layer: NPD collapses rich host signal into
	// booleans, and reasoning over them would mean reasoning over someone
	// else's summary of the evidence rather than the evidence.
	NPDConditions []RawJSON `json:"npdConditions,omitempty"`

	// CollectedAt is when the Kubernetes capture was taken.
	CollectedAt time.Time `json:"collectedAt"`
}

// Baseline records what existing tooling concluded about the same scenario.
type Baseline struct {
	// PrometheusAlerts lists alerts a stock Prometheus/Alertmanager setup had
	// firing at capture time.
	PrometheusAlerts []string `json:"prometheusAlerts,omitempty"`

	// K8sGPTOutput is what k8sgpt alone concluded, verbatim.
	K8sGPTOutput string `json:"k8sgptOutput,omitempty"`

	// Notes records anything else about incumbent tooling behaviour worth
	// capturing, such as an alert that fired only after a delay.
	Notes string `json:"notes,omitempty"`
}

// Label is the ground truth for a scenario.
//
// It is a separate type stored in a separate file (label.json) from the
// Fixture (fixture.json), and this is the single most important structural
// decision in the harness. The blinding claim rests on the loader having no
// code path that reads a label during analysis: LoadFixture does not read
// label.json and cannot return a Label, so no amount of careless wiring
// downstream can leak ground truth into the reasoning layer.
//
// Labels are loaded only by the scorer, only after verdicts are produced.
type Label struct {
	// ScenarioID must match the fixture's ScenarioID.
	ScenarioID string `json:"scenarioId"`

	// Relationship is the true relationship for this scenario.
	//
	// Negative controls carry coincidental, and are built in the same pass as
	// positives rather than afterwards: building all the positives first
	// invites unconscious tuning against them, and the false-correlation rate
	// then surfaces far too late to be cheap to fix.
	Relationship Relationship `json:"relationship"`

	// RootCauseLayer is the true layer, when the relationship is causal.
	RootCauseLayer *Layer `json:"rootCauseLayer,omitempty"`

	// FaultType identifies what was injected, for example
	// "disk.reallocated_growth" or "disk.io_timeout". Empty for a scenario
	// with no injected fault.
	FaultType string `json:"faultType,omitempty"`

	// Injection is the machine-readable description emitted by the injection
	// script that created this fault, carried through verbatim.
	Injection *Injection `json:"injection,omitempty"`

	// Notes is free text: what was done, what was expected, and anything about
	// the scenario a scorer or a later reader needs to know.
	Notes string `json:"notes,omitempty"`
}

// Injection is the machine-readable record of one injected fault, emitted by
// the injection script and stored in the label.
type Injection struct {
	// Tool is the injection mechanism, for example "dm-dust" or "fill".
	Tool string `json:"tool"`

	// Device is the target device path.
	Device string `json:"device"`

	// Params records the parameters the script was run with.
	Params map[string]string `json:"params,omitempty"`

	// StartedAt and EndedAt bound the injection window.
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
}
