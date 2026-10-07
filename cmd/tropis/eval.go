package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/00Webbo/tropis/eval/report"
	"github.com/00Webbo/tropis/eval/runner"
	"github.com/00Webbo/tropis/pkg/inventory"
	"github.com/00Webbo/tropis/pkg/reason/backend"
)

// backendFlags configure a reasoning backend on top of TROPIS_* environment
// variables; flags win.
type backendFlags struct {
	provider, model, baseURL, api, think, effort string
}

func (b *backendFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&b.provider, "backend", "", "reasoning backend: anthropic, local or mock (default from TROPIS_BACKEND, else anthropic)")
	fs.StringVar(&b.model, "model", "", "model name (default from TROPIS_MODEL, else the backend's default)")
	fs.StringVar(&b.baseURL, "base-url", "", "backend endpoint (default from TROPIS_BASE_URL)")
	fs.StringVar(&b.api, "local-api", "", "local backend protocol: ollama or openai (default from TROPIS_LOCAL_API)")
	fs.StringVar(&b.think, "think", "", "local (ollama) thinking: true, false, low, medium or high; empty is the model's default (default from TROPIS_LOCAL_THINK)")
	fs.StringVar(&b.effort, "effort", "", "anthropic effort level (default from TROPIS_EFFORT)")
}

func (b *backendFlags) config() (backend.Config, error) {
	cfg, err := backend.FromEnv()
	if err != nil {
		return cfg, err
	}
	for dst, src := range map[*string]string{
		&cfg.Provider: b.provider, &cfg.Model: b.model, &cfg.BaseURL: b.baseURL,
		&cfg.API: b.api, &cfg.Think: b.think, &cfg.Effort: b.effort,
	} {
		if src != "" {
			*dst = src
		}
	}
	return cfg, nil
}

func init() {
	commands["eval"] = command{"replay a fixture corpus and score the verdicts", runEval}
}

func runEval(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	corpus := fs.String("corpus", "eval/fixtures", "fixture corpus directory")
	out := fs.String("out", "", "output directory for results.json and report.md (default eval/results/<timestamp>)")
	seed := fs.Int64("seed", 0, "order randomisation seed (default: from the clock; always recorded)")
	publishable := fs.Bool("publishable", false, "refuse a corpus containing synthetic fixtures")
	inv := fs.String("inventory", "", "rig inventory, to describe the rig in the results")
	concurrency := fs.Int("concurrency", 4, "parallel analyses")
	quiet := fs.Bool("quiet", false, "no per-case progress")
	var bf backendFlags
	bf.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := bf.config()
	if err != nil {
		return fail(stderr, "%v", err)
	}
	b, err := backend.New(cfg)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	rc := runner.Config{
		Corpus:             *corpus,
		Backend:            b,
		Seed:               *seed,
		RequirePublishable: *publishable,
		Concurrency:        *concurrency,
	}
	if !*quiet {
		rc.Progress = stderr
	}
	if *inv != "" {
		i, err := inventory.Load(*inv)
		if err != nil {
			return fail(stderr, "%v", err)
		}
		rc.Inventory = i
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := runner.Run(ctx, rc)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	dir := *out
	if dir == "" {
		dir = filepath.Join("eval", "results", res.StartedAt.Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(stderr, "%v", err)
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(data, '\n'), 0o644); err != nil {
		return fail(stderr, "%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(report.Markdown(res)), 0o644); err != nil {
		return fail(stderr, "%v", err)
	}

	m := res.Metrics
	fmt.Fprintf(stdout, "%d fixtures in %s\n", m.Cases, res.FinishedAt.Sub(res.StartedAt).Round(time.Millisecond))
	fmt.Fprintf(stdout, "end-to-end detection %.1f%% (%d/%d), root-cause accuracy %.1f%% (%d/%d), pre-filter recall %.1f%%, false-correlation rate %.1f%% (%d/%d)\n",
		100*m.EndToEndDetection, m.EndToEndCorrect, m.Positives,
		100*m.RootCauseAccuracy, m.RootCauseCorrect, m.Positives,
		100*m.PrefilterRecall,
		100*m.FalseCorrelationRate, m.FalseCorrelations, m.NegativeControls)
	fmt.Fprintf(stdout, "gate: %s — %s\n", res.Gate.Status, res.Gate.Reason)
	for _, warn := range res.Warnings {
		fmt.Fprintf(stdout, "warning: %s\n", warn)
	}
	fmt.Fprintf(stdout, "wrote %s and %s\n", filepath.Join(dir, "results.json"), filepath.Join(dir, "report.md"))
	return exitOK
}
