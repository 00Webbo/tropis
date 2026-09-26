package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nathanwebb/tropis/eval/capture"
	"github.com/nathanwebb/tropis/eval/scenarios"
	"github.com/nathanwebb/tropis/pkg/inventory"
	"github.com/nathanwebb/tropis/pkg/k8s"
	"github.com/nathanwebb/tropis/pkg/schema"
)

func init() {
	commands["capture"] = command{"capture a fixture from a live node during an injected fault", runCapture}
}

const captureUsage = `usage: tropis capture --scenario <id> --variant npd-present|npd-absent
                      --node <name> --inventory <file> [--disk <id>]
                      [--injection <record.json>]
                      [--prometheus <url> | --alerts-file <file>] [--k8sgpt-file <file>]
                      [--out eval/fixtures] [--force]

Captures the node's host and Kubernetes state through the same collectors the
sweep uses, and writes <out>/<scenario>-<variant>/fixture.json and label.json.
Run it while the fault is active and its symptoms are present: start the
injection, wait, capture, then stop. See hack/inject/README.md.
`

func runCapture(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, captureUsage) }
	scenarioID := fs.String("scenario", "", "scenario id from eval/scenarios/scenarios.yaml")
	variant := fs.String("variant", "", "npd-present or npd-absent")
	node := fs.String("node", "", "node to capture")
	disk := fs.String("disk", "", "inventory disk id the fault was injected on")
	invFile := fs.String("inventory", "", "rig inventory")
	injection := fs.String("injection", "", "the injection script's record (its start record, when capturing during the fault)")
	prom := fs.String("prometheus", "", "Prometheus URL to read firing alerts from, for the baseline")
	alertsFile := fs.String("alerts-file", "", "file of firing alert names, one per line, for the baseline")
	k8sgptFile := fs.String("k8sgpt-file", "", "k8sgpt's output for the same node, for the baseline")
	notes := fs.String("baseline-notes", "", "free-text notes on incumbent tooling behaviour")
	out := fs.String("out", "eval/fixtures", "corpus directory")
	force := fs.Bool("force", false, "replace an existing capture")
	timeout := fs.Duration("timeout", 5*time.Minute, "overall timeout")
	var cf clusterFlags
	cf.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *scenarioID == "" || *variant == "" || *node == "" || *invFile == "" {
		fs.Usage()
		return exitUsage
	}
	if *prom != "" && *alertsFile != "" {
		return fail(stderr, "use --prometheus or --alerts-file, not both")
	}

	all, err := scenarios.Load()
	if err != nil {
		return fail(stderr, "%v", err)
	}
	s, err := scenarios.Find(all, *scenarioID)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	inv, err := inventory.Load(*invFile)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	var rec *capture.InjectionRecord
	if *injection != "" {
		if rec, err = capture.LoadInjectionRecord(*injection); err != nil {
			return fail(stderr, "%v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	baseline := schema.Baseline{Notes: *notes}
	switch {
	case *prom != "":
		if baseline.PrometheusAlerts, err = capture.PrometheusAlerts(ctx, *prom); err != nil {
			return fail(stderr, "%v", err)
		}
	case *alertsFile != "":
		if baseline.PrometheusAlerts, err = capture.ReadLines(*alertsFile); err != nil {
			return fail(stderr, "%v", err)
		}
	default:
		fmt.Fprintln(stderr, "tropis: warning: no Prometheus baseline; uplift over the incumbent cannot be measured for this fixture")
	}
	if *k8sgptFile != "" {
		b, err := os.ReadFile(*k8sgptFile)
		if err != nil {
			return fail(stderr, "%v", err)
		}
		baseline.K8sGPTOutput = string(b)
	}

	cs, _, err := cf.clients()
	if err != nil {
		return fail(stderr, "%v", err)
	}
	dir, err := capture.Capture(ctx, capture.Sources{
		Host: &k8s.HostFetcher{Client: cs, Namespace: cf.namespace, Selector: cf.selector},
		K8s:  &k8s.Collector{Client: cs},
	}, capture.Request{
		Scenario:  s,
		Variant:   schema.Variant(*variant),
		Node:      *node,
		Disk:      *disk,
		Inventory: inv,
		Injection: rec,
		Baseline:  baseline,
		OutDir:    *out,
		Force:     *force,
	})
	if err != nil {
		if errors.Is(err, capture.ErrRefused) {
			fmt.Fprintln(stderr, "tropis: nothing was written; the fixture would not be a true record of the scenario")
		}
		return fail(stderr, "%v", err)
	}
	fmt.Fprintf(stdout, "captured %s (%s) into %s\n", s.ID, *variant, dir)
	return exitOK
}
