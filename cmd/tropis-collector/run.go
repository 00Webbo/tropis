package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/00Webbo/tropis/pkg/host/prefilter"
	"github.com/00Webbo/tropis/pkg/host/smart"
)

const usage = `usage: tropis-collector <command> [flags] [device...]

commands:
  collect     read SMART and print the report as JSON
  prefilter   evaluate deterministic rules (--npd for NPD plugin protocol)
  serve       serve SMART reports over HTTP

Devices default to everything smartctl --scan finds.
`

// exit codes outside the NPD protocol
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 64
)

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "collect":
		return runCollect(args[1:], stdout, stderr)
	case "prefilter":
		return runPrefilter(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// sourceFlags are shared by every command that obtains a SMART report.
type sourceFlags struct {
	smartctl string
	timeout  time.Duration
	fromDir  string
}

func (s *sourceFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.smartctl, "smartctl", smart.DefaultBinary, "smartctl binary")
	fs.DurationVar(&s.timeout, "timeout", smart.DefaultTimeout, "per-device smartctl timeout")
	fs.StringVar(&s.fromDir, "from-dir", "",
		"read saved `smartctl -j` output (*.json) from this directory instead of invoking smartctl; for replay and testing")
}

// report obtains a SMART report, either live or from saved output.
func (s *sourceFlags) report(ctx context.Context, devices []string) (*smart.Report, error) {
	if s.fromDir != "" {
		return reportFromDir(s.fromDir)
	}
	c := &smart.Collector{Binary: s.smartctl, Timeout: s.timeout}
	if len(devices) > 0 {
		return c.Collect(ctx, devices)
	}
	return c.CollectAll(ctx)
}

// reportFromDir parses every *.json file in dir as smartctl output. The file
// name stands in for the device path when the output names none.
func reportFromDir(dir string) (*smart.Report, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no *.json files in %s", dir)
	}
	sort.Strings(matches)

	report := &smart.Report{CollectedAt: time.Now().UTC()}
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		fallback := "/dev/" + strings.TrimSuffix(filepath.Base(m), ".json")
		dev, fail := smart.Parse(data, fallback)
		if fail != nil {
			report.Failures = append(report.Failures, *fail)
			continue
		}
		report.Devices = append(report.Devices, *dev)
	}
	return report, nil
}

func runCollect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("collect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var src sourceFlags
	src.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	report, err := src.report(context.Background(), fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "collect: %v\n", err)
		return exitError
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "collect: %v\n", err)
		return exitError
	}
	return exitOK
}

func runPrefilter(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prefilter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var src sourceFlags
	src.register(fs)
	npd := fs.Bool("npd", false, "speak node-problem-detector's custom plugin protocol")
	stateDir := fs.String("state-dir", "", "directory holding previous readings, for growth rules; empty disables them")
	thresholdsFile := fs.String("thresholds", "", "JSON file overriding default thresholds")
	if err := fs.Parse(args); err != nil {
		if *npd {
			return prefilter.NPDUnknown
		}
		return exitUsage
	}

	// In NPD mode every failure is reported as Unknown with a short message:
	// NPD reads only the exit code and the first line, so a Go error or a
	// usage dump would be mangled into a nonsense condition.
	fail := func(format string, a ...any) int {
		msg := fmt.Sprintf(format, a...)
		if *npd {
			fmt.Fprintln(stdout, truncate(msg, prefilter.NPDMaxOutput))
			return prefilter.NPDUnknown
		}
		fmt.Fprintln(stderr, "prefilter: "+msg)
		return exitError
	}

	th := prefilter.DefaultThresholds()
	if *thresholdsFile != "" {
		data, err := os.ReadFile(*thresholdsFile)
		if err != nil {
			return fail("read thresholds: %v", err)
		}
		// Unmarshal over the defaults, so a file need only name the
		// thresholds it changes.
		if err := json.Unmarshal(data, &th); err != nil {
			return fail("parse thresholds: %v", err)
		}
	}

	report, err := src.report(context.Background(), fs.Args())
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fail("SMART collection timed out")
		}
		return fail("%v", err)
	}

	var previous map[string]*smart.Device
	if *stateDir != "" {
		previous, err = prefilter.LoadState(*stateDir)
		if err != nil {
			// Losing one growth comparison beats failing every run.
			fmt.Fprintf(stderr, "prefilter: %v\n", err)
		}
	}

	result := prefilter.Evaluate(report, previous, th)

	if *stateDir != "" {
		if err := prefilter.SaveState(*stateDir, previous, report); err != nil {
			fmt.Fprintf(stderr, "prefilter: %v\n", err)
		}
	}

	if *npd {
		code, msg := result.NPD()
		fmt.Fprintln(stdout, msg)
		return code
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return fail("%v", err)
	}
	return exitOK
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
