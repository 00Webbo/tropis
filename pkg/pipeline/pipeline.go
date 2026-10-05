// Package pipeline is the live diagnostic path: collect a node's host and
// Kubernetes state, pre-filter it, and ask the reasoning backend about the
// candidates.
//
// It is the same path the eval runner replays, through the same Prefilter,
// input builder and backend. The only difference is where the captures come
// from: live collectors here, fixture files there.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/00Webbo/tropis/pkg/host/prefilter"
	"github.com/00Webbo/tropis/pkg/host/smart"
	"github.com/00Webbo/tropis/pkg/k8s/triggers"
	"github.com/00Webbo/tropis/pkg/notify"
	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/schema"
)

// Prefilter runs the deterministic rules over raw captures exactly as the
// live sweep does: SMART rules against the capture and its previous reading,
// the Kubernetes-side storage rules (pkg/k8s/triggers) against the
// Kubernetes capture, plus node-problem-detector conditions as triggers
// where present.
func Prefilter(host schema.HostCapture, kc schema.K8sCapture, th prefilter.Thresholds) prefilter.Result {
	report := parseSMART(host.SMART, host.CollectedAt)
	var previous map[string]*smart.Device
	if host.Previous != nil {
		prev := parseSMART(host.Previous.SMART, host.Previous.CollectedAt)
		previous = map[string]*smart.Device{}
		for i := range prev.Devices {
			d := prev.Devices[i]
			previous[prefilter.DeviceKey(&d)] = &d
		}
	}
	res := prefilter.Evaluate(report, previous, th)

	var conds []prefilter.NPDCondition
	for _, raw := range kc.NPDConditions {
		var c prefilter.NPDCondition
		if json.Unmarshal(raw, &c) == nil {
			conds = append(conds, c)
		}
	}
	res.ApplyNPD(conds, prefilter.DefaultNPDTriggerTypes)
	res.KubernetesTriggers = triggers.RuleIDs(triggers.Evaluate(kc))
	return res
}

func parseSMART(raw map[string]schema.RawJSON, at time.Time) *smart.Report {
	paths := make([]string, 0, len(raw))
	for p := range raw {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	report := &smart.Report{CollectedAt: at}
	for _, p := range paths {
		d, fail := smart.Parse(raw[p], p)
		if fail != nil {
			report.Failures = append(report.Failures, *fail)
			continue
		}
		report.Devices = append(report.Devices, *d)
	}
	return report
}

// HostSource supplies a node's host capture.
type HostSource interface {
	Fetch(ctx context.Context, node string) (*schema.HostCapture, error)
}

// K8sSource supplies a node's Kubernetes capture, and the list of nodes a
// sweep visits.
//
// The capture carries the node's NPD conditions, split from the Node object,
// so the pre-filter needs nothing else from the API server.
type K8sSource interface {
	Collect(ctx context.Context, node string) (*schema.K8sCapture, error)
	Nodes(ctx context.Context) ([]string, error)
}

// ReportWriter persists a verdict, e.g. as a NodeHealthReport.
type ReportWriter interface {
	// Write stores the verdict and returns the one it replaced, or nil if
	// the node had none. The previous verdict is what notification compares
	// against.
	Write(ctx context.Context, v schema.Verdict) (previous *schema.Verdict, err error)
	// Existing returns the latest stored verdict for every node that has one.
	Existing(ctx context.Context) (map[string]schema.Verdict, error)
}

// Pipeline wires the collectors, pre-filter and backend together.
type Pipeline struct {
	Host       HostSource
	K8s        K8sSource
	Backend    reason.Backend
	Thresholds prefilter.Thresholds
	// Writer, when set, receives every verdict produced.
	Writer ReportWriter
	// Notifier, when set, is told about verdict changes the Policy deems
	// notable. It needs a Writer: without stored verdicts there is nothing
	// to compare against.
	Notifier notify.Notifier
	Policy   notify.Policy
}

// Outcome is what happened for one node.
type Outcome struct {
	Node        string          `json:"node"`
	Raised      bool            `json:"raised"`
	TriggeredBy []string        `json:"triggeredBy,omitempty"`
	Verdict     *schema.Verdict `json:"verdict,omitempty"`
	// Notified is the kind of change notified, if any.
	Notified notify.Kind `json:"notified,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// AnalyzeNode diagnoses one node.
//
// With force, the node is analysed whether or not the pre-filter raises it —
// the explicit `tropis analyze <node>` case. Without it, a node the
// pre-filter does not raise is left alone: sending every node's state to a
// model on every sweep is both expensive and harmful, since a model asked
// "is one causing the other" of two noisy streams is biased toward yes.
//
// The Kubernetes capture is taken before pre-filtering, because the
// Kubernetes-side rules read it, and the same capture is then analysed: a
// raised node is never fetched twice. Collect is bounded (one field-selected
// pod list, event lists, and log tails only for failing containers).
func (p *Pipeline) AnalyzeNode(ctx context.Context, node string, force bool) (Outcome, error) {
	out := Outcome{Node: node}

	host, err := p.Host.Fetch(ctx, node)
	if err != nil {
		return out, fmt.Errorf("host capture for %s: %w", node, err)
	}
	k8s, err := p.K8s.Collect(ctx, node)
	if err != nil {
		return out, fmt.Errorf("kubernetes capture for %s: %w", node, err)
	}
	pre := Prefilter(*host, *k8s, p.Thresholds)
	out.Raised = pre.Candidate()
	out.TriggeredBy = pre.TriggeredBy()
	if !out.Raised && !force {
		return out, nil
	}

	in, err := reason.BuildInput(reason.Request{
		Node:        node,
		Host:        *host,
		Kubernetes:  *k8s,
		TriggeredBy: out.TriggeredBy,
	})
	if err != nil {
		return out, err
	}
	v, err := p.Backend.Analyze(ctx, in)
	if err != nil {
		return out, fmt.Errorf("analyse %s: %w", node, err)
	}
	out.Verdict = &v

	if p.Writer == nil {
		return out, nil
	}
	previous, err := p.Writer.Write(ctx, v)
	if err != nil {
		return out, fmt.Errorf("write report for %s: %w", node, err)
	}
	if p.Notifier != nil {
		if change := p.Policy.Evaluate(previous, v); change != nil {
			out.Notified = change.Kind
			if err := p.Notifier.Notify(ctx, *change); err != nil {
				return out, fmt.Errorf("notify for %s: %w", node, err)
			}
		}
	}
	return out, nil
}

// Sweep pre-filters every node and analyses the candidates — or, with all,
// every node regardless, for a first baseline or a demo. A failure on one
// node is recorded in its outcome and does not stop the sweep.
//
// A node whose stored verdict is causal is always re-analysed, raised or
// not. Otherwise a node that recovered, and so stopped being raised, would
// keep its causal report forever, and anything alerting on that report
// would never clear.
func (p *Pipeline) Sweep(ctx context.Context, all bool) ([]Outcome, error) {
	nodes, err := p.K8s.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	var existing map[string]schema.Verdict
	if p.Writer != nil {
		if existing, err = p.Writer.Existing(ctx); err != nil {
			return nil, fmt.Errorf("read existing reports: %w", err)
		}
	}
	outcomes := make([]Outcome, 0, len(nodes))
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		prev, reported := existing[n]
		force := all || (reported && prev.Relationship == schema.RelationshipCausal)
		o, err := p.AnalyzeNode(ctx, n, force)
		if err != nil {
			o.Error = err.Error()
		}
		outcomes = append(outcomes, o)
	}
	return outcomes, nil
}
