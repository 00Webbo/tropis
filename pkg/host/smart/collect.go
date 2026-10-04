package smart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds a single smartctl invocation.
//
// A drive that hangs on a SMART query is itself a signal, and an unbounded
// wait would stall the whole sweep behind the one device most likely to be
// broken.
const DefaultTimeout = 30 * time.Second

// DefaultBinary is the smartctl executable, resolved via PATH.
const DefaultBinary = "smartctl"

// Collector reads SMART data by invoking smartctl.
type Collector struct {
	// Binary is the smartctl executable. Empty means DefaultBinary.
	Binary string

	// Timeout bounds each invocation. Zero means DefaultTimeout.
	Timeout time.Duration

	// runner executes a command and returns its stdout. Replaced in tests so
	// the collector's logic is testable without smartctl present.
	runner func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewCollector returns a Collector using the system smartctl.
func NewCollector() *Collector {
	return &Collector{Binary: DefaultBinary, Timeout: DefaultTimeout}
}

func (c *Collector) binary() string {
	if c.Binary == "" {
		return DefaultBinary
	}
	return c.Binary
}

func (c *Collector) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

func (c *Collector) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.runner != nil {
		return c.runner(ctx, c.binary(), args...)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, c.binary(), args...)
	out, err := cmd.Output()

	// A non-zero exit is normal and expected: smartctl's status is a bitfield
	// of findings, and the output is still valid JSON. Only a failure to run
	// the binary at all is an error here.
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) {
		return out, nil
	}
	return out, err
}

// Available reports whether smartctl can be executed.
//
// Checked separately from collection so the agent can distinguish "no disks
// are failing" from "we were never able to look", which are very different
// statements to put in front of an operator.
func (c *Collector) Available(ctx context.Context) error {
	if _, err := c.run(ctx, "--version"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s not found in PATH: install smartmontools", c.binary())
		}
		return fmt.Errorf("%s is not usable: %w", c.binary(), err)
	}
	return nil
}

// scanOutput is the shape of `smartctl --scan -j`.
type scanOutput struct {
	Devices []struct {
		Name     string `json:"name"`
		InfoName string `json:"info_name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"devices"`
}

// Scan lists the devices smartctl can see.
//
// Used when no explicit device list is configured. An explicit list is
// preferable in production: scanning picks up removable media and virtual
// devices that are noise at best.
func (c *Collector) Scan(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "--scan", "-j")
	if err != nil {
		return nil, fmt.Errorf("smartctl --scan: %w", err)
	}

	var parsed scanOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse smartctl --scan output: %w", err)
	}

	devices := make([]string, 0, len(parsed.Devices))
	for _, d := range parsed.Devices {
		if d.Name != "" {
			devices = append(devices, d.Name)
		}
	}
	return devices, nil
}

// CollectDevice reads SMART data for one device.
//
// It returns either a Device or a DeviceFailure, never both and never
// neither. A device that cannot be read is a finding in its own right, not an
// error to be swallowed.
func (c *Collector) CollectDevice(ctx context.Context, path string) (*Device, *DeviceFailure) {
	out, err := c.run(ctx, "-j", "-a", path)
	if err != nil {
		return nil, &DeviceFailure{
			Path:   path,
			Reason: classifyRunError(err),
			Detail: err.Error(),
		}
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, &DeviceFailure{
			Path:   path,
			Reason: FailureParse,
			Detail: "smartctl produced no output",
		}
	}
	return Parse(out, path)
}

// Collect reads SMART data for every named device.
//
// Devices are read in sequence rather than in parallel: SMART queries contend
// on the same controller, and a sweep is not latency-sensitive.
//
// A failure on one device never aborts the sweep — one unreadable disk must
// not hide the state of the others.
func (c *Collector) Collect(ctx context.Context, paths []string) (*Report, error) {
	if err := c.Available(ctx); err != nil {
		return nil, err
	}

	report := &Report{CollectedAt: time.Now().UTC()}

	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		dev, fail := c.CollectDevice(ctx, path)
		switch {
		case fail != nil:
			report.Failures = append(report.Failures, *fail)
		case dev != nil:
			report.Devices = append(report.Devices, *dev)
		}
	}
	return report, nil
}

// CollectAll scans for devices and reads all of them.
func (c *Collector) CollectAll(ctx context.Context) (*Report, error) {
	if err := c.Available(ctx); err != nil {
		return nil, err
	}
	paths, err := c.Scan(ctx)
	if err != nil {
		return nil, err
	}
	return c.Collect(ctx, paths)
}

// classifyRunError maps an execution error onto a FailureReason.
func classifyRunError(err error) FailureReason {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return FailureSmartctlMissing
	case errors.Is(err, context.DeadlineExceeded):
		return FailureTimeout
	case errors.Is(err, context.Canceled):
		return FailureOther
	default:
		return FailureOther
	}
}

// CollectRaw returns the verbatim `smartctl -j -a` output for each device,
// keyed by device path, exactly as a fixture stores it.
//
// Analysis consumes raw output rather than parsed Devices so that live
// analysis and fixture replay share one parsing path: a fixture is then a
// faithful stand-in for a live node, and the parser is exercised on replay.
//
// Devices whose output is empty are omitted; everything else, including
// smartctl's own error envelopes, is returned for the parser to classify.
func (c *Collector) CollectRaw(ctx context.Context, paths []string) (map[string][]byte, error) {
	if err := c.Available(ctx); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		scanned, err := c.Scan(ctx)
		if err != nil {
			return nil, err
		}
		paths = scanned
	}
	out := make(map[string][]byte, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		data, err := c.run(ctx, "-j", "-a", path)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		out[path] = data
	}
	return out, nil
}
