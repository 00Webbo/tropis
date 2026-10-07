// Package runner replays a fixture corpus through the full pipeline offline
// and scores the verdicts against ground truth.
//
// A run has two phases, and the order is the point:
//
//  1. Analysis. Every fixture is loaded (without its label — the loader
//     cannot read one), put through the pre-filter and the reasoning
//     backend, in a randomised order whose seed is recorded.
//  2. Scoring. Only once every verdict exists are labels loaded, and only
//     here.
//
// Nothing that runs in phase 1 has a path to ground truth. A test verifies
// both that no label is read before the last analysis finishes and that no
// label content ever appears in model input.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"github.com/00Webbo/tropis/pkg/fixture"
	"github.com/00Webbo/tropis/pkg/host/prefilter"
	"github.com/00Webbo/tropis/pkg/inventory"
	"github.com/00Webbo/tropis/pkg/pipeline"
	"github.com/00Webbo/tropis/pkg/reason"
	"github.com/00Webbo/tropis/pkg/schema"
)

// ResultsVersion identifies the results file format.
const ResultsVersion = "tropis.io/eval-results/v1alpha1"

// LabelSource loads ground truth for a case. The default reads label.json;
// tests substitute a spy to verify when labels are read.
type LabelSource func(fixture.Case) (*schema.Label, error)

// DefaultTimeout bounds one analysis when Config.Timeout is zero.
const DefaultTimeout = 5 * time.Minute

// Config configures a run.
type Config struct {
	// Corpus is the fixture directory.
	Corpus string

	// Backend produces verdicts.
	Backend reason.Backend

	// Seed randomises analysis order. Zero picks one from the clock; the seed
	// used is always recorded, so any run can be reproduced.
	Seed int64

	// RequirePublishable refuses a corpus containing synthetic fixtures. Set
	// it for any run whose numbers might be quoted.
	RequirePublishable bool

	// Thresholds configure the pre-filter. Zero value uses the defaults.
	Thresholds *prefilter.Thresholds

	// Concurrency bounds parallel analyses. Zero means 4.
	Concurrency int

	// Timeout bounds one analysis. Zero means DefaultTimeout.
	Timeout time.Duration

	// Inventory, when set, describes the rig in the results.
	Inventory *inventory.Inventory

	// Progress receives one line per analysed case. Nil discards.
	Progress io.Writer

	// Labels overrides how ground truth is loaded. Nil reads label.json.
	Labels LabelSource

	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
}

// Results is a complete run, serialised as results.json.
type Results struct {
	Version      string               `json:"version"`
	StartedAt    time.Time            `json:"startedAt"`
	FinishedAt   time.Time            `json:"finishedAt"`
	Seed         int64                `json:"seed"`
	Corpus       string               `json:"corpus"`
	Synthetic    bool                 `json:"synthetic"`
	AgentVersion string               `json:"agentVersion"`
	Backend      schema.BackendInfo   `json:"backend"`
	Thresholds   prefilter.Thresholds `json:"thresholds"`
	Rig          *Rig                 `json:"rig,omitempty"`
	Cases        []CaseResult         `json:"cases"`
	Metrics      Metrics              `json:"metrics"`
	ByVariant    map[string]Metrics   `json:"byVariant"`
	Gate         Gate                 `json:"gate"`
	Warnings     []string             `json:"warnings,omitempty"`
}

// Rig summarises the capture rig from the inventory.
type Rig struct {
	Cluster           string `json:"cluster"`
	KubernetesVersion string `json:"kubernetesVersion"`
	Distribution      string `json:"distribution,omitempty"`
	Nodes             int    `json:"nodes"`
}

// CaseResult is one fixture's outcome.
type CaseResult struct {
	// Order is the position in the randomised analysis order.
	Order      int    `json:"order"`
	ScenarioID string `json:"scenarioId"`
	Variant    string `json:"variant"`
	Dir        string `json:"dir"`

	// Raised reports whether the pre-filter would have sent this node for
	// analysis. Every fixture is analysed regardless, so reasoning accuracy
	// and pre-filter recall are measured separately.
	Raised      bool     `json:"raised"`
	TriggeredBy []string `json:"triggeredBy,omitempty"`

	Verdict   *schema.Verdict `json:"verdict,omitempty"`
	Error     string          `json:"error,omitempty"`
	ErrorKind string          `json:"errorKind,omitempty"`
	Duration  time.Duration   `json:"durationNs"`

	// Filled in phase 2.
	Expected Expected `json:"expected"`
	Score    Score    `json:"score"`
}

// Expected is the ground truth, recorded only after analysis.
type Expected struct {
	Relationship   schema.Relationship `json:"relationship"`
	RootCauseLayer *schema.Layer       `json:"rootCauseLayer,omitempty"`
	FaultType      string              `json:"faultType,omitempty"`
}

// Score grades one case.
type Score struct {
	// RelationshipCorrect: the verdict's relationship matches.
	RelationshipCorrect bool `json:"relationshipCorrect"`
	// Correct: relationship matches and, for causal, so does the layer. This
	// is what "correct root cause" means for a positive.
	Correct bool `json:"correct"`
	// FalseCorrelation: a causal verdict on a non-causal scenario.
	FalseCorrelation bool `json:"falseCorrelation"`
}

// Error kinds.
const (
	ErrKindMalformed = "malformed_output"
	ErrKindRefused   = "refused"
	ErrKindOther     = "error"
)

// Run executes an evaluation.
func Run(ctx context.Context, cfg Config) (*Results, error) {
	if cfg.Backend == nil {
		return nil, errors.New("runner: a backend is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	th := prefilter.DefaultThresholds()
	if cfg.Thresholds != nil {
		th = *cfg.Thresholds
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = now().UnixNano()
	}
	labels := cfg.Labels
	if labels == nil {
		labels = fixture.LoadLabelFor
	}

	load := fixture.LoadCorpus
	if cfg.RequirePublishable {
		load = fixture.PublishableCorpus
	}
	cases, err := load(cfg.Corpus)
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("runner: no fixtures in %s", cfg.Corpus)
	}

	res := &Results{
		Version:      ResultsVersion,
		StartedAt:    now().UTC(),
		Seed:         seed,
		Corpus:       cfg.Corpus,
		AgentVersion: reason.AgentVersion,
		Backend:      cfg.Backend.Describe(),
		Thresholds:   th,
	}
	if cfg.Inventory != nil {
		res.Rig = &Rig{
			Cluster:           cfg.Inventory.Cluster.Name,
			KubernetesVersion: cfg.Inventory.Cluster.KubernetesVersion,
			Distribution:      cfg.Inventory.Cluster.Distribution,
			Nodes:             len(cfg.Inventory.Nodes),
		}
	}
	for _, c := range cases {
		if c.Fixture.Synthetic {
			res.Synthetic = true
		}
	}

	// Randomise order, so nothing downstream can depend on corpus layout.
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(cases), func(i, j int) { cases[i], cases[j] = cases[j], cases[i] })

	// --- Phase 1: analysis. No label is reachable from here. -------------
	res.Cases = analyse(ctx, cfg, cases, th)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// --- Phase 2: scoring. Labels are loaded now, and only now. ----------
	for i := range res.Cases {
		l, err := labels(cases[i])
		if err != nil {
			return nil, fmt.Errorf("load label for %s: %w", cases[i].Dir, err)
		}
		res.Cases[i].Expected = Expected{
			Relationship:   l.Relationship,
			RootCauseLayer: l.RootCauseLayer,
			FaultType:      l.FaultType,
		}
		res.Cases[i].Score = score(res.Cases[i].Verdict, l)
	}

	res.Metrics = computeMetrics(res.Cases)
	res.ByVariant = map[string]Metrics{}
	for _, v := range []schema.Variant{schema.VariantNPDAbsent, schema.VariantNPDPresent} {
		var subset []CaseResult
		for _, c := range res.Cases {
			if c.Variant == string(v) {
				subset = append(subset, c)
			}
		}
		if len(subset) > 0 {
			res.ByVariant[string(v)] = computeMetrics(subset)
		}
	}
	res.Gate = evaluateGate(res)
	res.Warnings = warnings(res)
	res.FinishedAt = now().UTC()
	return res, nil
}

// analyse runs phase 1 with bounded concurrency, preserving the randomised
// order in the results.
func analyse(ctx context.Context, cfg Config, cases []fixture.Case, th prefilter.Thresholds) []CaseResult {
	workers := cfg.Concurrency
	if workers <= 0 {
		workers = 4
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	out := make([]CaseResult, len(cases))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var progress sync.Mutex

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out[i] = analyseOne(ctx, cfg.Backend, cases[i], i, th, timeout)
				if cfg.Progress != nil {
					progress.Lock()
					fmt.Fprintln(cfg.Progress, progressLine(out[i]))
					progress.Unlock()
				}
			}
		}()
	}
	for i := range cases {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

func progressLine(c CaseResult) string {
	if c.Verdict != nil {
		return fmt.Sprintf("%-50s %-12s %-22s %.2f", c.ScenarioID, c.Variant, c.Verdict.Relationship, c.Verdict.Confidence)
	}
	return fmt.Sprintf("%-50s %-12s ERROR %s", c.ScenarioID, c.Variant, c.ErrorKind)
}

func analyseOne(ctx context.Context, b reason.Backend, c fixture.Case, order int, th prefilter.Thresholds, timeout time.Duration) (r CaseResult) {
	f := c.Fixture
	r = CaseResult{Order: order, ScenarioID: f.ScenarioID, Variant: string(f.Variant), Dir: c.Dir}

	pre := pipeline.Prefilter(f.Host, f.Kubernetes, th)
	r.Raised = pre.Candidate()
	r.TriggeredBy = pre.TriggeredBy()

	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()

	in, err := reason.BuildInput(reason.RequestFromFixture(f, r.TriggeredBy))
	if err != nil {
		r.Error, r.ErrorKind = err.Error(), ErrKindOther
		return r
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	v, err := b.Analyze(actx, in)
	if err != nil {
		r.Error = err.Error()
		switch {
		case errors.Is(err, reason.ErrMalformedOutput):
			r.ErrorKind = ErrKindMalformed
		case errors.Is(err, reason.ErrRefused):
			r.ErrorKind = ErrKindRefused
		default:
			r.ErrorKind = ErrKindOther
		}
		return r
	}
	r.Verdict = &v
	return r
}

func score(v *schema.Verdict, l *schema.Label) Score {
	var s Score
	if v == nil {
		return s
	}
	s.RelationshipCorrect = v.Relationship == l.Relationship
	s.Correct = s.RelationshipCorrect
	if l.Relationship == schema.RelationshipCausal && s.Correct {
		s.Correct = v.RootCause != nil && l.RootCauseLayer != nil && v.RootCause.Layer == *l.RootCauseLayer
	}
	s.FalseCorrelation = l.Relationship != schema.RelationshipCausal && v.Relationship == schema.RelationshipCausal
	return s
}
