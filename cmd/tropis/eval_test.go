package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A longer --timeout must reach the backend's own request timeout, or the
// backend's default silently caps it; one set explicitly with TROPIS_TIMEOUT
// is the operator's choice and is kept. analyze, sweep (through
// clusterFlags.pipeline) and eval all build their backend through config.
func TestTimeoutReachesBackend(t *testing.T) {
	t.Setenv("TROPIS_TIMEOUT", "")
	var bf backendFlags
	cfg, err := bf.config(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 10*time.Minute {
		t.Errorf("backend timeout = %s, want --timeout's 10m", cfg.Timeout)
	}

	t.Setenv("TROPIS_TIMEOUT", "90s")
	cfg, err = bf.config(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 90*time.Second {
		t.Errorf("backend timeout = %s, want the explicit 90s", cfg.Timeout)
	}
}

func TestEvalTimeoutFlag(t *testing.T) {
	corpus := filepath.Join("..", "..", "eval", "testdata")
	if code, _, errOut := runCmd("eval", "--corpus", corpus, "--backend", "mock", "--timeout", "0s", "--out", t.TempDir()); code == exitOK || !strings.Contains(errOut, "--timeout") {
		t.Errorf("--timeout 0s: exit %d, %q", code, errOut)
	}
}

// analyze and sweep accept both spellings of the report flag.
func TestWriteReportSpellings(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-kubeconfig")
	for _, args := range [][]string{
		{"analyze", "n1", "--write-report"},
		{"analyze", "n1", "--write-reports"},
		{"sweep", "--write-report=false"},
		{"sweep", "--write-reports=false"},
	} {
		code, _, errOut := runCmd(append(args, "--kubeconfig", missing)...)
		if code == exitUsage {
			t.Errorf("%v: rejected as usage error: %s", args, errOut)
		}
	}
}
