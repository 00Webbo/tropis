package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/00Webbo/tropis/eval/runner"
	"github.com/00Webbo/tropis/pkg/reason/mock"
)

func TestMarkdownOnDevCorpus(t *testing.T) {
	res, err := runner.Run(context.Background(), runner.Config{
		Corpus:  filepath.Join("..", "testdata"),
		Backend: mock.New(),
		Seed:    11,
	})
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(res)

	for _, want := range []string{
		"DEVELOPMENT CORPUS — NOT PUBLISHABLE",
		"## Gate",
		"NOT APPLICABLE",
		"End-to-end detection",
		"Root-cause accuracy",
		"Pre-filter recall",
		"Pre-filter raised non-causal",
		"False-correlation rate",
		"npd-absent",
		"npd-present",
		"## Confusion",
		"## Calibration",
		"pending-sectors-postgres",
		"mock / heuristic-v1",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	// Every case appears.
	if got := strings.Count(md, "| npd-"); got < 60 {
		t.Errorf("report lists %d case rows, want 60", got)
	}
	if strings.Contains(md, "| Thinking |") || strings.Contains(md, "| Effort |") {
		t.Error("report shows backend settings that were not set")
	}

	// Settings that change results are shown with the run.
	res.Backend.Effort = "high"
	res.Backend.Think = "false"
	md = Markdown(res)
	for _, want := range []string{"| Effort | high |", "| Thinking | false |"} {
		if !strings.Contains(md, want) {
			t.Errorf("report is missing %q", want)
		}
	}
}
