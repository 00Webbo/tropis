package prefilter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/00Webbo/tropis/pkg/host/smart"
)

// NPD custom plugin protocol exit codes.
//
// node-problem-detector's CustomPluginMonitor runs a plugin and reads its exit
// code and stdout. These values are fixed by NPD, not chosen here.
const (
	NPDOK      = 0 // no problem found
	NPDProblem = 1 // problem found; stdout describes it
	NPDUnknown = 2 // could not determine
)

// NPDMaxOutput is the message length budget. NPD's example configuration sets
// max_output_length to 80, and output beyond it is truncated by NPD anyway.
// This is also why an NPD condition can only ever be a trigger: 80 characters
// cannot carry evidence.
const NPDMaxOutput = 80

// Result is the pre-filter's verdict on one host sweep.
type Result struct {
	// Findings are the rules that fired, in device then rule-priority order.
	Findings []Finding `json:"findings,omitempty"`

	// Unknown lists devices the pre-filter could not assess, with the reason.
	// A disk that cannot be read is not a healthy disk.
	Unknown []string `json:"unknown,omitempty"`

	// NPDTriggers lists node-problem-detector conditions that raised the node.
	// Recorded separately from Findings because they are triggers only, never
	// evidence — see NPDCondition.
	NPDTriggers []string `json:"npdTriggers,omitempty"`

	// KubernetesTriggers lists the Kubernetes-side rule IDs (k8s.*) that
	// raised the node. They are evaluated by pkg/k8s/triggers and merged in
	// by the pipeline; this package only carries them, so the host
	// collector, which also builds a Result, stays free of Kubernetes code.
	// They are not host findings and never appear in the NPD plugin output.
	KubernetesTriggers []string `json:"kubernetesTriggers,omitempty"`
}

// Candidate reports whether the node should be sent to the reasoning layer.
func (r Result) Candidate() bool {
	return len(r.Findings) > 0 || len(r.NPDTriggers) > 0 || len(r.KubernetesTriggers) > 0
}

// TriggeredBy returns the distinct rule IDs that raised the node, sorted, for
// Verdict.TriggeredBy.
func (r Result) TriggeredBy() []string {
	seen := map[string]bool{}
	for _, f := range r.Findings {
		seen[f.RuleID] = true
	}
	for _, t := range r.NPDTriggers {
		seen[t] = true
	}
	for _, t := range r.KubernetesTriggers {
		seen[t] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// NPD renders the result in NPD custom plugin form: an exit code and a
// message of at most NPDMaxOutput bytes.
//
// A problem takes precedence over an unknown: if one disk is failing and
// another could not be read, the node has a problem.
func (r Result) NPD() (int, string) {
	switch {
	case len(r.Findings) > 0:
		return NPDProblem, truncate(summarise(r.Findings), NPDMaxOutput)
	case len(r.Unknown) > 0:
		return NPDUnknown, truncate("SMART unreadable: "+strings.Join(r.Unknown, ", "), NPDMaxOutput)
	default:
		return NPDOK, "SMART OK"
	}
}

// summarise groups findings by device, highest-priority first, so the most
// decisive signal survives truncation.
func summarise(findings []Finding) string {
	var order []string
	byDev := map[string][]string{}
	for _, f := range findings {
		name := strings.TrimPrefix(f.Device, "/dev/")
		if _, ok := byDev[name]; !ok {
			order = append(order, name)
		}
		byDev[name] = append(byDev[name], f.Summary)
	}
	parts := make([]string, 0, len(order))
	for _, dev := range order {
		parts = append(parts, dev+": "+strings.Join(byDev[dev], ", "))
	}
	return strings.Join(parts, "; ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "..."
	return s[:n-len(ellipsis)] + ellipsis
}

// Evaluate runs the rules over a whole sweep.
//
// previous maps a device key (see DeviceKey) to its last reading; it may be
// nil, in which case growth rules stay silent.
func Evaluate(report *smart.Report, previous map[string]*smart.Device, th Thresholds) Result {
	var res Result
	if report == nil {
		return res
	}
	for i := range report.Devices {
		cur := &report.Devices[i]
		var prev *smart.Device
		if previous != nil {
			prev = previous[DeviceKey(cur)]
		}
		res.Findings = append(res.Findings, EvaluateDevice(cur, prev, th)...)
	}
	for _, f := range report.Failures {
		res.Unknown = append(res.Unknown, fmt.Sprintf("%s (%s)", strings.TrimPrefix(f.Path, "/dev/"), f.Reason))
	}
	return res
}

// DeviceKey identifies a physical device across readings.
//
// Keyed on model and serial rather than path: device paths are not stable
// across reboots, and a replaced drive under the same path must not be read
// as a healthy drive whose defect count went down. Falls back to the path
// when the device reports no serial.
func DeviceKey(d *smart.Device) string {
	if d.SerialNumber != "" {
		return d.ModelName + "/" + d.SerialNumber
	}
	return "path:" + d.Path
}

// NPDCondition is a node condition reported by node-problem-detector.
//
// It is defined here, rather than importing the Kubernetes API types, so the
// host collector carries no Kubernetes dependency — it holds no API
// permissions and has no reason to link the client.
type NPDCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// DefaultNPDTriggerTypes are the NPD condition types that raise a node.
//
// Only conditions plausibly related to the host layer are listed. NPD
// conditions are an optional, cheap "look at this node" signal where NPD is
// installed. They never reach the reasoning layer as evidence: NPD collapses
// rich host signal into a boolean, and reasoning over it would mean reasoning
// over someone else's summary of the evidence.
var DefaultNPDTriggerTypes = []string{
	"KernelDeadlock",
	"ReadonlyFilesystem",
	"FrequentKubeletRestart",
	"FrequentDockerRestart",
	"FrequentContainerdRestart",
	"CorruptDockerOverlay2",
}

// ApplyNPD adds NPD-sourced triggers to a result. It is a no-op when NPD is
// absent, which is the point: everything works identically without it.
func (r *Result) ApplyNPD(conditions []NPDCondition, triggerTypes []string) {
	allowed := map[string]bool{}
	for _, t := range triggerTypes {
		allowed[t] = true
	}
	for _, c := range conditions {
		if c.Status == "True" && allowed[c.Type] {
			r.NPDTriggers = append(r.NPDTriggers, "npd."+c.Type)
		}
	}
}
