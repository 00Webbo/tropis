// Package mock is a deterministic, rule-based stand-in for a model backend.
//
// It exists for tests, for the kind-cluster install check, and for exercising
// the eval harness without spending tokens. It is not a reasoning engine and
// its accuracy numbers mean nothing about Tropis's: it pattern-matches a few
// obvious signals so the pipeline has something plausible to carry.
//
// It deliberately produces its answer as model-output JSON and passes it
// through reason.Finalize, exactly as a real backend does, so everything
// downstream of the model is exercised for real.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/schema"
)

// Model is the mock's model identifier.
const Model = "heuristic-v1"

// Backend is the mock backend.
type Backend struct{}

// New returns a mock backend.
func New() *Backend { return &Backend{} }

// Describe identifies the mock.
func (b *Backend) Describe() schema.BackendInfo {
	return schema.BackendInfo{Provider: "mock", Model: Model, PromptVersion: reason.PromptVersion}
}

var (
	ioError = regexp.MustCompile(`(?i)input/output error|\bEIO\b|i/o error|read-only file system|could not fsync|blk_update_request|medium error|buffer i/o error`)
	// DiskPressure is read from the node condition, whose status says whether
	// it holds; matching the word in free text would read the event
	// "NodeHasNoDiskPressure" as exhaustion.
	exhausted = regexp.MustCompile(`(?i)no space left on device|low on resource: ephemeral-storage|evictionthresholdmet`)
)

// Analyze applies the heuristic and returns a validated verdict.
func (b *Backend) Analyze(ctx context.Context, in *reason.AnalysisInput) (schema.Verdict, error) {
	if err := ctx.Err(); err != nil {
		return schema.Verdict{}, err
	}
	doc, err := in.Document()
	if err != nil {
		return schema.Verdict{}, err
	}

	out := decide(doc)
	raw, err := json.Marshal(out)
	if err != nil {
		return schema.Verdict{}, err
	}
	return reason.Finalize(raw, in, b.Describe())
}

func decide(doc reason.Document) reason.ModelOutput {
	var activeHost, stableHost []reason.ModelEvidence
	for _, d := range doc.Host.Devices {
		if ev, ok := activeFault(d); ok {
			activeHost = append(activeHost, ev)
		} else if ev, ok := stableAnomaly(d); ok {
			stableHost = append(stableHost, ev)
		}
	}
	for _, u := range doc.Host.Unreadable {
		activeHost = append(activeHost, reason.ModelEvidence{Ref: u.Ref, Excerpt: "unreadable: " + u.Reason})
	}

	var ioSymptoms, exhaustion, otherSymptoms []reason.ModelEvidence
	for _, l := range doc.Kubernetes.Logs {
		for i, line := range l.Lines {
			ref := fmt.Sprintf("%s#L%d", l.Ref, i+1)
			switch {
			case ioError.MatchString(line):
				ioSymptoms = append(ioSymptoms, reason.ModelEvidence{Ref: ref, Excerpt: line})
			case exhausted.MatchString(line):
				exhaustion = append(exhaustion, reason.ModelEvidence{Ref: ref, Excerpt: line})
			}
		}
	}
	for _, e := range doc.Kubernetes.Events {
		text := e.Reason + " " + e.Message
		switch {
		case ioError.MatchString(text):
			ioSymptoms = append(ioSymptoms, reason.ModelEvidence{Ref: e.Ref, Excerpt: e.Message})
		case exhausted.MatchString(text):
			exhaustion = append(exhaustion, reason.ModelEvidence{Ref: e.Ref, Excerpt: e.Message})
		}
	}
	for _, c := range doc.Kubernetes.NodeConditions {
		if c.Type == "DiskPressure" && c.Status == "True" {
			exhaustion = append(exhaustion, reason.ModelEvidence{Ref: c.Ref, Excerpt: "DiskPressure True"})
		}
	}
	for _, p := range doc.Kubernetes.Pods {
		if s, ok := podSymptom(p); ok {
			otherSymptoms = append(otherSymptoms, reason.ModelEvidence{Ref: p.Ref, Excerpt: s})
		}
	}

	switch {
	case len(activeHost) > 0 && len(ioSymptoms) > 0:
		return reason.ModelOutput{
			Reasoning:    "An actively degrading disk coincides with I/O errors in the workload.",
			Relationship: string(schema.RelationshipCausal),
			RootCause:    &reason.ModelRootCause{Layer: string(schema.LayerHost), Description: "A degrading disk is producing I/O errors in workloads on this node."},
			Confidence:   0.8,
			Evidence:     first(4, activeHost, ioSymptoms),
			NextStep:     "Consider a planned drain of this node and replacement of the affected disk.",
		}
	case len(activeHost) == 0 && len(exhaustion) > 0:
		return reason.ModelOutput{
			Reasoning:    "The disk hardware looks healthy while the node reports storage exhaustion.",
			Relationship: string(schema.RelationshipCausal),
			RootCause:    &reason.ModelRootCause{Layer: string(schema.LayerKubernetes), Description: "A workload is exhausting node storage."},
			Confidence:   0.6,
			Evidence:     first(4, exhaustion, otherSymptoms),
			NextStep:     "Consider finding which workload is consuming node storage.",
		}
	case len(activeHost)+len(stableHost) > 0 && len(otherSymptoms) > 0 && len(ioSymptoms) == 0:
		return reason.ModelOutput{
			Reasoning:    "A disk anomaly and a workload problem are both present, but nothing in the workload points to the disk.",
			Relationship: string(schema.RelationshipCoincidental),
			Confidence:   0.6,
			Evidence:     first(4, append(activeHost, stableHost...), otherSymptoms),
			NextStep:     "Investigate the workload failure on its own terms; keep monitoring the disk.",
		}
	default:
		ev := first(4, activeHost, stableHost, ioSymptoms, otherSymptoms)
		if len(ev) == 0 {
			ev = anyRef(doc)
		}
		return reason.ModelOutput{
			Reasoning:    "The signals present do not connect the layers either way.",
			Relationship: string(schema.RelationshipInsufficientEvidence),
			Confidence:   0.5,
			Evidence:     ev,
			NextStep:     "Re-check on the next sweep.",
		}
	}
}

// activeFault reports a disk that is failing now or getting worse.
func activeFault(d reason.DeviceDoc) (reason.ModelEvidence, bool) {
	ev := func(s string) (reason.ModelEvidence, bool) {
		return reason.ModelEvidence{Ref: d.Ref, Excerpt: s}, true
	}
	c, p := d.Current, d.Previous
	switch {
	case d.HealthPassed != nil && !*d.HealthPassed:
		return ev("overall health FAILED")
	case c.PendingSectors != nil && *c.PendingSectors > 0:
		return ev(fmt.Sprintf("pendingSectors %d", *c.PendingSectors))
	case c.CriticalWarning != nil && *c.CriticalWarning != 0:
		return ev(fmt.Sprintf("criticalWarning %d", *c.CriticalWarning))
	case p != nil && grew(c.ReallocatedSectors, p.ReallocatedSectors):
		return ev(fmt.Sprintf("reallocatedSectors %d, was %d", *c.ReallocatedSectors, *p.ReallocatedSectors))
	case p != nil && grew(c.UncorrectableErrors, p.UncorrectableErrors):
		return ev(fmt.Sprintf("uncorrectableErrors %d, was %d", *c.UncorrectableErrors, *p.UncorrectableErrors))
	}
	return reason.ModelEvidence{}, false
}

// stableAnomaly reports a defect that is present but not changing.
func stableAnomaly(d reason.DeviceDoc) (reason.ModelEvidence, bool) {
	c := d.Current
	if c.ReallocatedSectors != nil && *c.ReallocatedSectors > 0 {
		return reason.ModelEvidence{Ref: d.Ref, Excerpt: fmt.Sprintf("reallocatedSectors %d, not growing", *c.ReallocatedSectors)}, true
	}
	if c.UncorrectableErrors != nil && *c.UncorrectableErrors > 0 {
		return reason.ModelEvidence{Ref: d.Ref, Excerpt: fmt.Sprintf("uncorrectableErrors %d, not growing", *c.UncorrectableErrors)}, true
	}
	return reason.ModelEvidence{}, false
}

func grew(cur, prev *uint64) bool { return cur != nil && prev != nil && *cur > *prev }

func podSymptom(p reason.PodDoc) (string, bool) {
	switch p.Phase {
	case "Failed":
		return strings.TrimSpace("phase Failed " + p.Reason), true
	case "Succeeded":
		return "", false // a finished Job is not a symptom
	}
	for _, c := range p.Containers {
		if c.RestartCount == 0 && strings.HasPrefix(c.State, "terminated: Completed exit 0") {
			continue
		}
		if c.RestartCount > 0 || !c.Ready {
			return fmt.Sprintf("%s: %d restarts, %s", c.Name, c.RestartCount, c.State), true
		}
	}
	return "", false
}

func first(n int, groups ...[]reason.ModelEvidence) []reason.ModelEvidence {
	var out []reason.ModelEvidence
	seen := map[string]bool{}
	for _, g := range groups {
		for _, e := range g {
			if len(out) == n {
				return out
			}
			if !seen[e.Ref] && strings.TrimSpace(e.Excerpt) != "" {
				seen[e.Ref] = true
				out = append(out, e)
			}
		}
	}
	return out
}

// anyRef cites something that exists, for a verdict with nothing notable.
func anyRef(doc reason.Document) []reason.ModelEvidence {
	for _, d := range doc.Host.Devices {
		return []reason.ModelEvidence{{Ref: d.Ref, Excerpt: "no active fault in SMART data"}}
	}
	for _, p := range doc.Kubernetes.Pods {
		return []reason.ModelEvidence{{Ref: p.Ref, Excerpt: "phase " + p.Phase}}
	}
	return nil
}
