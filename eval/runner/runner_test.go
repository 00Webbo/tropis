package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nathanwebb/tropis/pkg/fixture"
	"github.com/nathanwebb/tropis/pkg/reason"
	"github.com/nathanwebb/tropis/pkg/reason/mock"
	"github.com/nathanwebb/tropis/pkg/schema"
)

var devCorpus = filepath.Join("..", "testdata")

// spyBackend wraps the mock, recording every input it sees and when.
type spyBackend struct {
	mu     sync.Mutex
	events *[]string
	inputs []string
	inner  reason.Backend
}

func (s *spyBackend) Describe() schema.BackendInfo { return s.inner.Describe() }

func (s *spyBackend) Analyze(ctx context.Context, in *reason.AnalysisInput) (schema.Verdict, error) {
	text, err := in.Text()
	if err != nil {
		return schema.Verdict{}, err
	}
	msg, _ := reason.UserMessage(in)
	s.mu.Lock()
	s.inputs = append(s.inputs, string(text)+msg)
	s.mu.Unlock()
	v, err := s.inner.Analyze(ctx, in)
	s.mu.Lock()
	*s.events = append(*s.events, "analyze")
	s.mu.Unlock()
	return v, err
}

// copyCorpus copies the dev corpus so a test can modify labels.
func copyCorpus(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(devCorpus, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(devCorpus, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// The T10 acceptance criterion: label withholding, verified two ways.
func TestLabelsAreWithheldFromAnalysis(t *testing.T) {
	corpus := copyCorpus(t)

	// Plant a unique marker in every label.
	const marker = "GROUND-TRUTH-MARKER-7f3a9c"
	labels, _ := filepath.Glob(filepath.Join(corpus, "*", fixture.LabelFile))
	if len(labels) == 0 {
		t.Fatal("no labels found")
	}
	for _, p := range labels {
		var l schema.Label
		data, _ := os.ReadFile(p)
		if err := json.Unmarshal(data, &l); err != nil {
			t.Fatal(err)
		}
		l.Notes = marker + " " + l.Notes
		out, _ := json.Marshal(l)
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var events []string
	var mu sync.Mutex
	spy := &spyBackend{events: &events, inner: mock.New()}
	res, err := Run(context.Background(), Config{
		Corpus:      corpus,
		Backend:     spy,
		Seed:        42,
		Concurrency: 8,
		Labels: func(c fixture.Case) (*schema.Label, error) {
			mu.Lock()
			events = append(events, "label")
			mu.Unlock()
			return fixture.LoadLabelFor(c)
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 1. Ordering: every analysis completes before the first label is read.
	firstLabel := -1
	lastAnalyze := -1
	for i, e := range events {
		if e == "label" && firstLabel < 0 {
			firstLabel = i
		}
		if e == "analyze" {
			lastAnalyze = i
		}
	}
	if firstLabel < 0 || lastAnalyze < 0 {
		t.Fatalf("events = %v", events)
	}
	if firstLabel < lastAnalyze {
		t.Errorf("a label was read (event %d) before analysis finished (event %d)", firstLabel, lastAnalyze)
	}

	// 2. Content: no ground truth reached any model input.
	if len(spy.inputs) != len(res.Cases) {
		t.Fatalf("backend saw %d inputs, want %d", len(spy.inputs), len(res.Cases))
	}
	for _, in := range spy.inputs {
		if strings.Contains(in, marker) {
			t.Fatal("label content reached model input")
		}
		if strings.Contains(in, "SYNTHETIC development fixture") {
			t.Fatal("label notes reached model input")
		}
	}

	// And the scores came from the labels.
	if res.Cases[0].Expected.Relationship == "" {
		t.Error("labels were not applied in scoring")
	}
}

func TestRunDevCorpus(t *testing.T) {
	res, err := Run(context.Background(), Config{Corpus: devCorpus, Backend: mock.New(), Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cases) != 60 {
		t.Errorf("cases = %d", len(res.Cases))
	}
	if !res.Synthetic {
		t.Error("the dev corpus must be flagged synthetic")
	}
	if res.Gate.Status != "not_applicable" {
		t.Errorf("gate on a synthetic corpus = %+v", res.Gate)
	}
	if len(res.ByVariant) != 2 {
		t.Errorf("byVariant = %v", res.ByVariant)
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "DEVELOPMENT CORPUS") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	m := res.Metrics
	if m.Positives != 40 || m.NegativeControls != 12 {
		t.Errorf("positives %d, negative controls %d", m.Positives, m.NegativeControls)
	}
	// The mock must exercise every answer, or the eval of it proves nothing.
	for _, rel := range []string{"causal", "coincidental", "insufficient_evidence"} {
		if m.Predicted[rel] == 0 {
			t.Errorf("mock never answered %s over the dev corpus", rel)
		}
	}
	if res.Backend.Provider != "mock" || res.Backend.PromptVersion == "" {
		t.Errorf("backend = %+v", res.Backend)
	}
}

// The same seed gives the same order; the order really is shuffled.
func TestOrderIsRandomisedAndReproducible(t *testing.T) {
	order := func(seed int64) []string {
		res, err := Run(context.Background(), Config{Corpus: devCorpus, Backend: mock.New(), Seed: seed, Concurrency: 1})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, c := range res.Cases {
			ids = append(ids, c.ScenarioID+"/"+c.Variant)
		}
		return ids
	}
	a, b, c := order(1), order(1), order(2)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Error("the same seed produced different orders")
	}
	if strings.Join(a, ",") == strings.Join(c, ",") {
		t.Error("different seeds produced the same order")
	}
	loaded, _ := fixture.LoadCorpus(devCorpus)
	var sorted []string
	for _, l := range loaded {
		sorted = append(sorted, l.Fixture.ScenarioID+"/"+string(l.Fixture.Variant))
	}
	if strings.Join(a, ",") == strings.Join(sorted, ",") {
		t.Error("order was not shuffled")
	}
}

func TestRequirePublishableRejectsDevCorpus(t *testing.T) {
	_, err := Run(context.Background(), Config{Corpus: devCorpus, Backend: mock.New(), RequirePublishable: true})
	if err == nil || !strings.Contains(err.Error(), "synthetic") {
		t.Errorf("err = %v", err)
	}
}

// failingBackend always errors, to check errors are scored and surfaced.
type failingBackend struct{ err error }

func (f failingBackend) Describe() schema.BackendInfo {
	return schema.BackendInfo{Provider: "failing", Model: "x", PromptVersion: "v1"}
}
func (f failingBackend) Analyze(context.Context, *reason.AnalysisInput) (schema.Verdict, error) {
	return schema.Verdict{}, f.err
}

func TestErrorsAreScoredAsWrong(t *testing.T) {
	res, err := Run(context.Background(), Config{Corpus: devCorpus, Backend: failingBackend{reason.ErrMalformedOutput}, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Metrics
	if m.RootCauseAccuracy != 0 || m.Errors[ErrKindMalformed] != 60 {
		t.Errorf("accuracy %v, errors %v", m.RootCauseAccuracy, m.Errors)
	}
	if m.Confusion["causal"][predictedError] != 40 {
		t.Errorf("confusion = %v", m.Confusion)
	}
	joined := strings.Join(res.Warnings, " ")
	for _, want := range []string{"never answered coincidental", "never answered insufficient_evidence", "produced no verdict"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings should include %q: %v", want, res.Warnings)
		}
	}
}

func TestRunNeedsBackendAndCorpus(t *testing.T) {
	if _, err := Run(context.Background(), Config{Corpus: devCorpus}); err == nil {
		t.Error("missing backend should be an error")
	}
	if _, err := Run(context.Background(), Config{Corpus: t.TempDir(), Backend: mock.New()}); err == nil {
		t.Error("empty corpus should be an error")
	}
}
