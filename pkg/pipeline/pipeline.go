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

	"github.com/nathanwebb/tropis/pkg/host/prefilter"
	"github.com/nathanwebb/tropis/pkg/host/smart"
	"github.com/nathanwebb/tropis/pkg/reason"
	"github.com/nathanwebb/tropis/pkg/schema"
)

// Prefilter runs the deterministic rules over raw captures exactly as the
// live sweep does: SMART rules against the capture and its previous reading,
// plus node-problem-detector conditions as triggers where present.
func Prefilter(host schema.HostCapture, npd []schema.RawJSON, th prefilter.Thresholds) prefilter.Result {
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
	for _, raw := range npd {
		var c prefilter.NPDCondition
		if json.Unmarshal(raw, &c) == nil {
			conds = append(conds, c)
		}
	}
	res.ApplyNPD(conds, prefilter.DefaultNPDTriggerTypes)
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

// K8sSource supplies a node's Kubernetes capture, and the cheaper node-only
// view a sweep needs to pre-filter.
type K8sSource interface {
	Collect(ctx context.Context, node string) (*schema.K8sCapture, error)
	NPDConditions(ctx context.Context, node string) ([]schema.RawJSON, error)
	Nodes(ctx context.Context) ([]string, error)
}

// ReportWriter persists a verdict, e.g. as a NodeHealthReport.
type ReportWriter interface {
	Write(ctx context.Context, v schema.Verdict) error
}

// Pipeline wires the collectors, pre-filter and backend together.
type Pipeline struct {
	Host       HostSource
	K8s        K8sSource
	Backend    reason.Backend
	Thresholds prefilter.Thresholds
	// Writer, when set, receives every verdict produced.
	Writer ReportWriter
}

// Outcome is what happened for one node.
type Outcome struct {
	Node        string          `json:"node"`
	Raised      bool            `json:"raised"`
	TriggeredBy []string        `json:"triggeredBy,omitempty"`
	Verdict     *schema.Verdict `json:"verdict,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// AnalyzeNode diagnoses one node.
//
// With force, the node is analysed whether or not the pre-filter raises it —
// the explicit `tropis analyze <node>` case. Without it, a node the
// pre-filter does not raise is left alone: sending every node's state to a
// model on every sweep is both expensive and harmful, since a model asked
// "is one causing the other" of two noisy streams is biased toward yes.
func (p *Pipeline) AnalyzeNode(ctx context.Context, node string, force bool) (Outcome, error) {
	out := Outcome{Node: node}

	host, err := p.Host.Fetch(ctx, node)
	if err != nil {
		return out, fmt.Errorf("host capture for %s: %w", node, err)
	}
	npd, err := p.K8s.NPDConditions(ctx, node)
	if err != nil {
		return out, fmt.Errorf("node conditions for %s: %w", node, err)
	}
	pre := Prefilter(*host, npd, p.Thresholds)
	out.Raised = pre.Candidate()
	out.TriggeredBy = pre.TriggeredBy()
	if !out.Raised && !force {
		return out, nil
	}

	k8s, err := p.K8s.Collect(ctx, node)
	if err != nil {
		return out, fmt.Errorf("kubernetes capture for %s: %w", node, err)
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

	if p.Writer != nil {
		if err := p.Writer.Write(ctx, v); err != nil {
			return out, fmt.Errorf("write report for %s: %w", node, err)
		}
	}
	return out, nil
}

// Sweep pre-filters every node and analyses the candidates — or, with all,
// every node regardless, for a first baseline or a demo. A failure on one
// node is recorded in its outcome and does not stop the sweep.
func (p *Pipeline) Sweep(ctx context.Context, all bool) ([]Outcome, error) {
	nodes, err := p.K8s.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	outcomes := make([]Outcome, 0, len(nodes))
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		o, err := p.AnalyzeNode(ctx, n, all)
		if err != nil {
			o.Error = err.Error()
		}
		outcomes = append(outcomes, o)
	}
	return outcomes, nil
}
