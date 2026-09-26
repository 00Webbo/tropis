package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nathanwebb/tropis/eval/runner"
	"github.com/nathanwebb/tropis/pkg/reason/mock"
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
		"Root-cause accuracy",
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
}
