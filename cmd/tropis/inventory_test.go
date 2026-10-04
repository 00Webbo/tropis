package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/00Webbo/tropis/pkg/schema"
)

func runCmd(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

var example = filepath.Join("..", "..", "hack", "inventory.example.yaml")

func TestInventoryValidate(t *testing.T) {
	code, out, errOut := runCmd("inventory", "validate", example)
	if code != exitOK || !strings.Contains(out, "3 nodes, 1 destructible disk") {
		t.Errorf("exit %d, %q %q", code, out, errOut)
	}
}

func TestInventoryTarget(t *testing.T) {
	code, out, _ := runCmd("inventory", "target", "--inventory", example, "--node", "worker-02", "--disk", "sacrificial")
	if code != exitOK || strings.TrimSpace(out) != "/dev/disk/by-id/ata-EXAMPLE_HDD_2TB_SERIAL0001" {
		t.Errorf("exit %d, %q", code, out)
	}
	code, out, _ = runCmd("inventory", "target", "--inventory", example, "--node", "worker-02", "--disk", "sacrificial", "--field", "mountpoint")
	if code != exitOK || strings.TrimSpace(out) != "/mnt/disks/sacrificial" {
		t.Errorf("mountpoint: exit %d, %q", code, out)
	}
}

// A protected disk yields an error and prints nothing a script could use.
func TestInventoryTargetRefusesProtected(t *testing.T) {
	code, out, errOut := runCmd("inventory", "target", "--inventory", example, "--node", "worker-02", "--disk", "system")
	if code == exitOK {
		t.Fatal("a non-destructible disk must not be returned as a target")
	}
	if out != "" {
		t.Errorf("nothing may be printed to stdout on refusal, got %q", out)
	}
	if !strings.Contains(errOut, "not marked destructible") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestInventoryValidateBadFile(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("apiVersion: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCmd("inventory", "validate", bad); code == exitOK {
		t.Error("invalid inventory should fail")
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := runCmd(); code != exitUsage {
		t.Errorf("no args: exit %d", code)
	}
	if code, _, errOut := runCmd("frobnicate"); code != exitUsage || !strings.Contains(errOut, "inventory") {
		t.Errorf("unknown command: exit %d, %q", code, errOut)
	}
}

func TestInventoryNode(t *testing.T) {
	code, out, _ := runCmd("inventory", "node", "--inventory", example, "--node", "worker-02")
	if code != exitOK || !strings.HasPrefix(out, "worker-02\tworker\t192.0.2.12") {
		t.Errorf("exit %d, %q", code, out)
	}
	if code, _, _ := runCmd("inventory", "node", "--inventory", example, "--node", "ghost"); code == exitOK {
		t.Error("unknown node should fail")
	}
}

func TestEvalCommand(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runCmd("eval", "--corpus", filepath.Join("..", "..", "eval", "testdata"),
		"--backend", "mock", "--out", out, "--seed", "5", "--quiet",
		"--inventory", example)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, f := range []string{"results.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if !strings.Contains(stdout, "DEVELOPMENT CORPUS") || !strings.Contains(stdout, "gate: not_applicable") {
		t.Errorf("stdout = %s", stdout)
	}
	report, _ := os.ReadFile(filepath.Join(out, "report.md"))
	if !strings.Contains(string(report), "tropis-lab, Kubernetes v1.31.2, 3 nodes") {
		t.Error("the rig from the inventory should be described in the report")
	}
}

func TestEvalPublishableRefusesDevCorpus(t *testing.T) {
	code, _, stderr := runCmd("eval", "--corpus", filepath.Join("..", "..", "eval", "testdata"),
		"--backend", "mock", "--out", t.TempDir(), "--publishable", "--quiet")
	if code == exitOK || !strings.Contains(stderr, "synthetic") {
		t.Errorf("exit %d, %s", code, stderr)
	}
}

func TestHumanOutput(t *testing.T) {
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	out := Human(schema.Verdict{
		Node: "worker-03", ObservedAt: at, Relationship: schema.RelationshipCausal, Confidence: 0.87,
		RootCause: &schema.RootCause{Layer: schema.LayerHost, Description: "Disk /dev/sdb is failing."},
		Evidence:  []schema.Evidence{{Source: "smartctl", Ref: "smart:/dev/sdb#197", Excerpt: "pendingSectors 24"}},
		NextStep:  "Consider replacing /dev/sdb.",
		Backend:   schema.BackendInfo{Provider: "anthropic", Model: "claude-opus-5", PromptVersion: "v1"},
	})
	for _, want := range []string{"CAUSAL (confidence 0.87)", "Root cause: host", "smart:/dev/sdb#197", "Tropis has taken no action", "anthropic/claude-opus-5"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAnalyzeUsage(t *testing.T) {
	if code, _, _ := runCmd("analyze"); code != exitUsage {
		t.Errorf("analyze without a node: exit %d", code)
	}
}

func TestCaptureUsage(t *testing.T) {
	code, _, stderr := runCmd("capture", "--scenario", "read-errors-postgres")
	if code != exitUsage || !strings.Contains(stderr, "--variant") {
		t.Errorf("exit %d, %q", code, stderr)
	}
	code, _, stderr = runCmd("capture", "--scenario", "no-such", "--variant", "npd-absent",
		"--node", "worker-02", "--inventory", example)
	if code == exitOK || !strings.Contains(stderr, "no scenario") {
		t.Errorf("unknown scenario: exit %d, %q", code, stderr)
	}
}

func TestNotifyConfigFromEnv(t *testing.T) {
	t.Setenv("TROPIS_SLACK_WEBHOOK_URL", "https://hooks.slack.example/T/B/x")
	t.Setenv("TROPIS_EVENTS", "true")
	t.Setenv("TROPIS_NOTIFY_ON", "any")
	c, err := notifyConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.policy != "any" || !c.events || c.slackURL == "" {
		t.Errorf("config = %+v", c)
	}
	if n, ok := c.notifiers(nil).(interface{ Name() string }); !ok || n.Name() != "notifiers" {
		t.Errorf("notifiers = %v", c.notifiers(nil))
	}

	t.Setenv("TROPIS_NOTIFY_ON", "sometimes")
	if _, err := notifyConfigFromEnv(); err == nil {
		t.Error("bad policy should be an error")
	}
}

func TestNoNotifiersByDefault(t *testing.T) {
	for _, k := range []string{"TROPIS_SLACK_WEBHOOK_URL", "TROPIS_WEBHOOK_URL", "TROPIS_EVENTS", "TROPIS_NOTIFY_ON"} {
		t.Setenv(k, "")
	}
	c, err := notifyConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.notifiers(nil) != nil {
		t.Error("nothing configured should mean no notifiers")
	}
}
