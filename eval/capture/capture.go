// Package capture turns a live node, mid-fault, into a fixture and its label.
//
// It reads the node through the same collectors the sweep uses, so a fixture
// is exactly what Tropis would have seen, stored raw so replay exercises the
// real parsers. The label is assembled from the scenario definition and the
// injection script's stop record, and must agree with both.
//
// Capture refuses to write a fixture that would lie about itself. The
// accuracy numbers are the project's differentiator, and the corpus is what
// they rest on: a fixture labelled with a SMART-visible fault the counters
// never showed, or claiming an NPD variant the node did not have, would
// quietly corrupt them.
package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nathanwebb/tropis/eval/scenarios"
	"github.com/nathanwebb/tropis/pkg/fixture"
	"github.com/nathanwebb/tropis/pkg/host/smart"
	"github.com/nathanwebb/tropis/pkg/inventory"
	"github.com/nathanwebb/tropis/pkg/pipeline"
	"github.com/nathanwebb/tropis/pkg/schema"
)

// InjectionRecord is what a hack/inject script prints on start or stop.
type InjectionRecord struct {
	FaultType string            `json:"faultType"`
	Injection *schema.Injection `json:"injection"`
}

// Request describes one capture.
type Request struct {
	Scenario scenarios.Scenario
	Variant  schema.Variant
	Node     string
	// Disk is the inventory disk id the fault was injected on.
	Disk      string
	Inventory *inventory.Inventory
	// Injection is the injection script's record — normally its start record,
	// since capture happens while the fault is active — or nil for a scenario
	// with no injected fault.
	Injection *InjectionRecord
	Baseline  schema.Baseline
	// OutDir is the corpus root; the fixture goes in <scenario>-<variant>.
	OutDir string
	// Force overwrites an existing capture.
	Force bool
}

// Sources are the live collectors.
type Sources struct {
	Host pipeline.HostSource
	K8s  pipeline.K8sSource
	Now  func() time.Time
}

// ErrRefused marks a capture refused because the fixture would be untrue.
var ErrRefused = errors.New("capture refused")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, a...)...)
}

// Dir is where a scenario variant's fixture lives.
func Dir(root, scenario string, variant schema.Variant) string {
	return filepath.Join(root, scenario+"-"+string(variant))
}

// Capture reads the node and writes fixture.json and label.json. It returns
// the directory written.
func Capture(ctx context.Context, src Sources, req Request) (string, error) {
	now := src.Now
	if now == nil {
		now = time.Now
	}
	if !req.Variant.Valid() {
		return "", fmt.Errorf("variant must be %q or %q", schema.VariantNPDAbsent, schema.VariantNPDPresent)
	}
	if req.Inventory == nil {
		return "", errors.New("an inventory is required: it records what the fixture was captured on")
	}
	node, err := req.Inventory.Node(req.Node)
	if err != nil {
		return "", err
	}
	var disk *inventory.Disk
	if req.Disk != "" {
		if _, disk, err = req.Inventory.Disk(req.Node, req.Disk); err != nil {
			return "", err
		}
	}

	label, err := buildLabel(req, now().UTC())
	if err != nil {
		return "", err
	}

	dir := Dir(req.OutDir, req.Scenario.ID, req.Variant)
	if _, err := os.Stat(filepath.Join(dir, fixture.FixtureFile)); err == nil && !req.Force {
		return "", fmt.Errorf("%s already holds a capture; pass --force to replace it", dir)
	}

	host, err := src.Host.Fetch(ctx, req.Node)
	if err != nil {
		return "", fmt.Errorf("host capture: %w", err)
	}
	k8s, err := src.K8s.Collect(ctx, req.Node)
	if err != nil {
		return "", fmt.Errorf("kubernetes capture: %w", err)
	}

	// The NPD variant is a claim about the node; check it against the node.
	hasNPD := len(k8s.NPDConditions) > 0
	switch {
	case req.Variant == schema.VariantNPDPresent && !hasNPD:
		return "", refuse("variant is npd-present but the node carries no node-problem-detector conditions; is NPD installed and running?")
	case req.Variant == schema.VariantNPDAbsent && hasNPD:
		return "", refuse("variant is npd-absent but the node carries %d non-kubelet conditions; uninstall NPD and clear its conditions first", len(k8s.NPDConditions))
	}

	f := &schema.Fixture{
		Version:     schema.FixtureVersion,
		ScenarioID:  req.Scenario.ID,
		Variant:     req.Variant,
		CapturedAt:  now().UTC(),
		Node:        req.Node,
		Environment: environment(req.Inventory, node, disk, k8s, host, req.Variant),
		Host:        *host,
		Kubernetes:  *k8s,
		Baseline:    req.Baseline,
		Synthetic:   false,
	}
	if err := f.Validate(); err != nil {
		return "", fmt.Errorf("captured fixture is invalid: %w", err)
	}

	if err := writeJSON(filepath.Join(dir, fixture.FixtureFile), f); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(dir, fixture.LabelFile), label); err != nil {
		return "", err
	}

	// Read both back through the same loaders the eval uses.
	loaded, err := fixture.LoadFixture(dir)
	if err != nil {
		return "", fmt.Errorf("written fixture does not load: %w", err)
	}
	if _, err := fixture.LoadLabelFor(fixture.Case{Dir: dir, Fixture: loaded}); err != nil {
		return "", fmt.Errorf("written label does not load: %w", err)
	}
	return dir, nil
}

// buildLabel assembles ground truth from the scenario and the injection
// record, refusing any combination that would make the label untrue.
func buildLabel(req Request, now time.Time) (*schema.Label, error) {
	s := req.Scenario
	l := s.Label()

	switch {
	case s.Inject != nil && req.Injection == nil:
		return nil, fmt.Errorf("scenario %s injects %s; pass the injection script's record with --injection", s.ID, s.Inject.Script)
	case s.Inject == nil && req.Injection != nil:
		return nil, fmt.Errorf("scenario %s has no injected fault, but an injection record was given", s.ID)
	}

	if rec := req.Injection; rec != nil {
		if rec.Injection == nil {
			return nil, errors.New("injection record has no injection block")
		}
		if rec.FaultType != s.FaultType {
			return nil, refuse("the injection was %s but scenario %s expects %s", rec.FaultType, s.ID, s.FaultType)
		}
		// Capture belongs while the fault is active — start, wait for
		// symptoms, capture, then stop — so a start record, with no endedAt,
		// is the normal case. A fault that began after the capture is a
		// mixed-up record or a wrong clock.
		if rec.Injection.StartedAt.IsZero() || rec.Injection.StartedAt.After(now) {
			return nil, refuse("the injection record starts at %s, after this capture; is it from another run?",
				rec.Injection.StartedAt.Format(time.RFC3339))
		}
		// A scenario defined as SMART-visible needs SMART to have been
		// affected for real. An emulated fault in its place would be a
		// synthetic fixture wearing a real one's label.
		if hasHardware(s, "real-sata") && rec.Injection.Params["smartEffect"] != "real" {
			return nil, refuse("scenario %s requires a real SMART effect, but the injection recorded smartEffect=%q",
				s.ID, rec.Injection.Params["smartEffect"])
		}
		l.Injection = rec.Injection
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

func hasHardware(s scenarios.Scenario, h string) bool {
	for _, x := range s.Hardware {
		if x == h {
			return true
		}
	}
	return false
}

// environment records the rig. What the live node reports wins over the
// inventory, which may be stale; the inventory fills the gaps.
func environment(inv *inventory.Inventory, node *inventory.Node, disk *inventory.Disk,
	k8s *schema.K8sCapture, host *schema.HostCapture, variant schema.Variant) schema.Environment {
	env := schema.Environment{
		Kernel:            node.Kernel,
		OSRelease:         node.OSRelease,
		KubernetesVersion: inv.Cluster.KubernetesVersion,
		ContainerRuntime:  inv.Cluster.ContainerRuntime,
		NPDPresent:        variant == schema.VariantNPDPresent,
	}
	var n corev1.Node
	if !k8s.NodeJSON.IsEmpty() && json.Unmarshal(k8s.NodeJSON, &n) == nil {
		info := n.Status.NodeInfo
		env.Kernel = firstNonEmpty(info.KernelVersion, env.Kernel)
		env.OSRelease = firstNonEmpty(info.OSImage, env.OSRelease)
		env.KubernetesVersion = firstNonEmpty(info.KubeletVersion, env.KubernetesVersion)
		env.ContainerRuntime = firstNonEmpty(info.ContainerRuntimeVersion, env.ContainerRuntime)
	}
	if disk != nil {
		env.DiskModel = disk.Model
		env.DiskTransport = disk.Transport
		// The device's own identity beats the inventory's description.
		if raw, ok := host.SMART[disk.Device]; ok {
			if d, fail := smart.Parse(raw, disk.Device); fail == nil {
				env.DiskModel = firstNonEmpty(d.ModelName, env.DiskModel)
				if d.Transport != smart.TransportUnknown {
					env.DiskTransport = string(d.Transport)
				}
			}
		}
	}
	return env
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// LoadInjectionRecord reads a record written by a hack/inject script.
// When a file holds both the start and stop records, the last one wins.
func LoadInjectionRecord(path string) (*InjectionRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec *InjectionRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r InjectionRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s: not an injection record: %w", path, err)
		}
		rec = &r
	}
	if rec == nil {
		return nil, fmt.Errorf("%s holds no injection record", path)
	}
	return rec, nil
}
