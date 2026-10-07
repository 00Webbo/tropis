package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A longer per-case timeout must reach the backend's own request timeout,
// or the backend's default silently caps it; one set explicitly with
// TROPIS_TIMEOUT is the operator's choice and is kept.
func TestCaseTimeoutReachesBackend(t *testing.T) {
	t.Setenv("TROPIS_TIMEOUT", "")
	var bf backendFlags
	cfg, err := bf.config()
	if err != nil {
		t.Fatal(err)
	}
	if got := withCaseTimeout(cfg, 10*time.Minute).Timeout; got != 10*time.Minute {
		t.Errorf("backend timeout = %s, want the per-case 10m", got)
	}

	t.Setenv("TROPIS_TIMEOUT", "90s")
	cfg, err = bf.config()
	if err != nil {
		t.Fatal(err)
	}
	if got := withCaseTimeout(cfg, 10*time.Minute).Timeout; got != 90*time.Second {
		t.Errorf("backend timeout = %s, want the explicit 90s", got)
	}
}

func TestEvalTimeoutFlag(t *testing.T) {
	corpus := filepath.Join("..", "..", "eval", "testdata")
	if code, _, errOut := runCmd("eval", "--corpus", corpus, "--backend", "mock", "--timeout", "0s", "--out", t.TempDir()); code == exitOK || !strings.Contains(errOut, "--timeout") {
		t.Errorf("--timeout 0s: exit %d, %q", code, errOut)
	}

	out := t.TempDir()
	code, _, errOut := runCmd("eval", "--corpus", corpus, "--backend", "mock", "--timeout", "10m", "--quiet", "--seed", "1", "--out", out)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(out, "results.json")); err != nil {
		t.Error(err)
	}
}
