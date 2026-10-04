package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/00Webbo/tropis/pkg/host/prefilter"
	"github.com/00Webbo/tropis/pkg/k8s"
	"github.com/00Webbo/tropis/pkg/pipeline"
	"github.com/00Webbo/tropis/pkg/reason/backend"
	"github.com/00Webbo/tropis/pkg/schema"
)

func init() {
	commands["analyze"] = command{"diagnose one node", runAnalyze}
	commands["sweep"] = command{"pre-filter every node and diagnose the candidates", runSweep}
}

// clusterFlags locate the cluster and the collector.
type clusterFlags struct {
	kubeconfig string
	namespace  string
	selector   string
}

func (c *clusterFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.kubeconfig, "kubeconfig", "", "kubeconfig file (default: KUBECONFIG, ~/.kube/config, then in-cluster)")
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = "tropis-system"
	}
	fs.StringVar(&c.namespace, "namespace", ns, "namespace the tropis-collector DaemonSet runs in")
	sel := os.Getenv("TROPIS_COLLECTOR_SELECTOR")
	if sel == "" {
		sel = k8s.DefaultCollectorSelector
	}
	fs.StringVar(&c.selector, "collector-selector", sel, "label selector for tropis-collector pods")
}

func (c *clusterFlags) clients() (kubernetes.Interface, dynamic.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.kubeconfig != "" {
		rules.ExplicitPath = c.kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("kubernetes config: %w", err)
	}
	cfg.UserAgent = "tropis/" + agentVersion()
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cs, dyn, nil
}

func (c *clusterFlags) pipeline(bf *backendFlags, write bool) (*pipeline.Pipeline, kubernetes.Interface, error) {
	cs, dyn, err := c.clients()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := bf.config()
	if err != nil {
		return nil, nil, err
	}
	b, err := backend.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	p := &pipeline.Pipeline{
		Host:       &k8s.HostFetcher{Client: cs, Namespace: c.namespace, Selector: c.selector},
		K8s:        &k8s.Collector{Client: cs},
		Backend:    b,
		Thresholds: prefilter.DefaultThresholds(),
	}
	if write {
		p.Writer = &k8s.ReportWriter{Client: dyn}
	}
	return p, cs, nil
}

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print the verdict as JSON: the same document a NodeHealthReport carries as .status")
	write := fs.Bool("write-report", false, "also write the verdict as the node's NodeHealthReport")
	timeout := fs.Duration("timeout", 5*time.Minute, "overall timeout")
	var cf clusterFlags
	var bf backendFlags
	cf.register(fs)
	bf.register(fs)
	// Accept `tropis analyze <node> --json` as well as flags first.
	node, rest := splitPositional(args)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if node == "" && fs.NArg() == 1 {
		node = fs.Arg(0)
	}
	if node == "" {
		fmt.Fprintln(stderr, "usage: tropis analyze <node> [--json] [--write-report]")
		return exitUsage
	}

	p, _, err := cf.pipeline(&bf, *write)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// An explicit request is always analysed, raised or not.
	out, err := p.AnalyzeNode(ctx, node, true)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out.Verdict); err != nil {
			return fail(stderr, "%v", err)
		}
		return exitOK
	}
	fmt.Fprint(stdout, Human(*out.Verdict))
	return exitOK
}

func runSweep(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	fs.SetOutput(stderr)
	write := fs.Bool("write-reports", true, "write each verdict as a NodeHealthReport")
	jsonOut := fs.Bool("json", false, "print every outcome as JSON")
	all := fs.Bool("all", false, "analyse every node, not only those the pre-filter raises; costs a model call per node")
	timeout := fs.Duration("timeout", 30*time.Minute, "overall timeout")
	var cf clusterFlags
	var bf backendFlags
	cf.register(fs)
	bf.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	nc, err := notifyConfigFromEnv()
	if err != nil {
		return fail(stderr, "%v", err)
	}
	p, cs, err := cf.pipeline(&bf, *write)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if n := nc.notifiers(cs); n != nil {
		// Changes are found by comparing with the stored report, so there
		// is nothing to notify about without one.
		if !*write {
			return fail(stderr, "notifications need --write-reports: changes are detected against the stored NodeHealthReport")
		}
		p.Notifier, p.Policy = n, nc.policy
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	outcomes, err := p.Sweep(ctx, *all)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(outcomes)
	} else {
		for _, o := range outcomes {
			switch {
			case o.Error != "":
				fmt.Fprintf(stdout, "%-24s error: %s\n", o.Node, o.Error)
			case o.Verdict != nil:
				line := fmt.Sprintf("%-24s %s (%.2f)", o.Node, o.Verdict.Relationship, o.Verdict.Confidence)
				if len(o.TriggeredBy) > 0 {
					line += ", raised by " + strings.Join(o.TriggeredBy, ", ")
				}
				if o.Notified != "" {
					line += ", notified (" + string(o.Notified) + ")"
				}
				fmt.Fprintln(stdout, line)
			default:
				fmt.Fprintf(stdout, "%-24s not raised\n", o.Node)
			}
		}
	}
	for _, o := range outcomes {
		if o.Error != "" {
			// A sweep with any failed node exits non-zero, so the CronJob's
			// status shows it.
			return exitError
		}
	}
	return exitOK
}

// splitPositional pulls a leading non-flag argument out, so flags may follow
// it; the standard flag package stops at the first positional.
func splitPositional(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// Human renders a verdict for a terminal.
func Human(v schema.Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Node %s — %s (confidence %.2f)\n\n", v.Node, strings.ToUpper(string(v.Relationship)), v.Confidence)
	if v.RootCause != nil {
		fmt.Fprintf(&b, "  Root cause: %s — %s\n\n", v.RootCause.Layer, wrap(v.RootCause.Description, 70, "  "))
	}
	b.WriteString("  Evidence:\n")
	for _, e := range v.Evidence {
		fmt.Fprintf(&b, "    %-15s %-38s %s\n", e.Source, e.Ref, e.Excerpt)
	}
	if v.NextStep != "" {
		fmt.Fprintf(&b, "\n  Next step (advisory): %s\n  Tropis has taken no action.\n", wrap(v.NextStep, 70, "  "))
	}
	fmt.Fprintf(&b, "\n  %s/%s, prompt %s, observed %s\n\n", v.Backend.Provider, v.Backend.Model, v.Backend.PromptVersion, v.ObservedAt.Format(time.RFC3339))
	return b.String()
}

func wrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	var lines []string
	var line string
	for _, w := range words {
		if len(line)+len(w)+1 > width && line != "" {
			lines = append(lines, line)
			line = w
			continue
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+indent)
}
