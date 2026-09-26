package capture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/00Webbo/tropis/eval/scenarios"
	"github.com/00Webbo/tropis/pkg/fixture"
	"github.com/00Webbo/tropis/pkg/inventory"
	"github.com/00Webbo/tropis/pkg/schema"
)

var at = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

type fakeHost struct{ raw schema.RawJSON }

func (f fakeHost) Fetch(context.Context, string) (*schema.HostCapture, error) {
	return &schema.HostCapture{SMART: map[string]schema.RawJSON{"/dev/sdb": f.raw}, CollectedAt: at}, nil
}

type fakeK8s struct{ npd bool }

func (f fakeK8s) Collect(context.Context, string) (*schema.K8sCapture, error) {
	c := &schema.K8sCapture{
		NodeJSON: schema.RawJSON(`{"metadata":{"name":"worker-02"},"status":{"nodeInfo":{"kernelVersion":"6.8.0-50-generic",` +
			`"osImage":"Ubuntu 24.04.1 LTS","kubeletVersion":"v1.31.4","containerRuntimeVersion":"containerd://1.7.24"}}}`),
		Pods:        []schema.RawJSON{schema.RawJSON(`{"metadata":{"namespace":"tropis-eval","name":"postgres-0"}}`)},
		CollectedAt: at,
	}
	if f.npd {
		c.NPDConditions = []schema.RawJSON{schema.RawJSON(`{"type":"KernelDeadlock","status":"False"}`)}
	}
	return c, nil
}
func (fakeK8s) NPDConditions(context.Context, string) ([]schema.RawJSON, error) { return nil, nil }
func (fakeK8s) Nodes(context.Context) ([]string, error)                         { return nil, nil }

func sources(t *testing.T, npd bool) Sources {
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "host", "smart", "testdata", "sata-failing.json"))
	if err != nil {
		t.Fatal(err)
	}
	return Sources{Host: fakeHost{raw: raw}, K8s: fakeK8s{npd: npd}, Now: func() time.Time { return at }}
}

func inv(t *testing.T) *inventory.Inventory {
	i, err := inventory.Load(filepath.Join("..", "..", "hack", "inventory.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func scenario(t *testing.T, id string) scenarios.Scenario {
	all, err := scenarios.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := scenarios.Find(all, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func record(faultType, smartEffect string, ended bool) *InjectionRecord {
	inj := &schema.Injection{Tool: "hdparm", Device: "/dev/sdb", StartedAt: at.Add(-time.Hour),
		Params: map[string]string{"smartEffect": smartEffect}}
	if ended {
		inj.EndedAt = at.Add(-time.Minute)
	}
	return &InjectionRecord{FaultType: faultType, Injection: inj}
}

func TestCapture(t *testing.T) {
	out := t.TempDir()
	dir, err := Capture(context.Background(), sources(t, false), Request{
		Scenario:  scenario(t, "pending-sectors-postgres"),
		Variant:   schema.VariantNPDAbsent,
		Node:      "worker-02",
		Disk:      "sacrificial",
		Inventory: inv(t),
		Injection: record("disk.pending_sectors", "real", true),
		Baseline:  schema.Baseline{PrometheusAlerts: []string{"KubePodCrashLooping"}},
		OutDir:    out,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if dir != filepath.Join(out, "pending-sectors-postgres-npd-absent") {
		t.Errorf("dir = %s", dir)
	}

	f, err := fixture.LoadFixture(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Synthetic {
		t.Error("a captured fixture must not be marked synthetic")
	}
	env := f.Environment
	// Live node info wins over the inventory; the device's identity wins
	// over the inventory's description of it.
	if env.Kernel != "6.8.0-50-generic" || env.KubernetesVersion != "v1.31.4" {
		t.Errorf("environment = %+v", env)
	}
	if env.DiskModel != "ST2000DM001-1ER164" || env.DiskTransport != "sata" {
		t.Errorf("disk = %s / %s", env.DiskModel, env.DiskTransport)
	}
	l, err := fixture.LoadLabelFor(fixture.Case{Dir: dir, Fixture: f})
	if err != nil {
		t.Fatal(err)
	}
	if l.Relationship != schema.RelationshipCausal || l.Injection == nil || l.FaultType != "disk.pending_sectors" {
		t.Errorf("label = %+v", l)
	}
	// A real capture is publishable.
	if _, err := fixture.PublishableCorpus(out); err != nil {
		t.Errorf("a real capture should be publishable: %v", err)
	}
}

// Each of these would write a fixture that lies about itself.
func TestCaptureRefusals(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		variant  schema.Variant
		npd      bool
		inj      *InjectionRecord
		want     string
		refused  bool
	}{
		{"emulated fault for a real-sata scenario", "pending-sectors-postgres", schema.VariantNPDAbsent, false,
			record("disk.pending_sectors", "emulated", true), "smartEffect", true},
		{"wrong fault type", "pending-sectors-postgres", schema.VariantNPDAbsent, false,
			record("disk.read_errors", "real", true), "expects disk.pending_sectors", true},
		{"injection started after the capture", "pending-sectors-postgres", schema.VariantNPDAbsent, false,
			future(record("disk.pending_sectors", "real", false)), "after this capture", true},
		{"npd-present without NPD", "read-errors-postgres", schema.VariantNPDPresent, false,
			record("disk.read_errors", "none", true), "no node-problem-detector", true},
		{"npd-absent with NPD", "read-errors-postgres", schema.VariantNPDAbsent, true,
			record("disk.read_errors", "none", true), "uninstall NPD", true},
		{"injected scenario without a record", "read-errors-postgres", schema.VariantNPDAbsent, false,
			nil, "--injection", false},
		{"record for a scenario with no fault", "stable-defects-config-crash", schema.VariantNPDAbsent, false,
			record("disk.read_errors", "none", true), "no injected fault", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := t.TempDir()
			_, err := Capture(context.Background(), sources(t, tt.npd), Request{
				Scenario: scenario(t, tt.scenario), Variant: tt.variant, Node: "worker-02", Disk: "sacrificial",
				Inventory: inv(t), Injection: tt.inj, OutDir: out,
			})
			if err == nil {
				t.Fatal("expected the capture to be refused")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
			if errors.Is(err, ErrRefused) != tt.refused {
				t.Errorf("ErrRefused = %v, want %v (%v)", errors.Is(err, ErrRefused), tt.refused, err)
			}
			if entries, _ := os.ReadDir(out); len(entries) != 0 {
				t.Error("a refused capture must write nothing")
			}
		})
	}
}

func TestCaptureWillNotOverwrite(t *testing.T) {
	out := t.TempDir()
	req := Request{
		Scenario: scenario(t, "stable-defects-config-crash"), Variant: schema.VariantNPDAbsent,
		Node: "worker-02", Disk: "sacrificial", Inventory: inv(t), OutDir: out,
	}
	if _, err := Capture(context.Background(), sources(t, false), req); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), sources(t, false), req); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("second capture: %v", err)
	}
	req.Force = true
	if _, err := Capture(context.Background(), sources(t, false), req); err != nil {
		t.Errorf("forced capture: %v", err)
	}
}

func TestLoadInjectionRecord(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fault.json")
	start := `{"faultType":"disk.read_errors","injection":{"tool":"dm-dust","device":"/dev/sdb","params":{},"startedAt":"2026-10-01T08:00:00Z"}}`
	stop := `{"faultType":"disk.read_errors","injection":{"tool":"dm-dust","device":"/dev/sdb","params":{},"startedAt":"2026-10-01T08:00:00Z","endedAt":"2026-10-01T08:30:00Z"}}`
	if err := os.WriteFile(p, []byte(start+"\n"+stop+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := LoadInjectionRecord(p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Injection.EndedAt.IsZero() {
		t.Error("the last record, the stop record, should win")
	}
}

func future(r *InjectionRecord) *InjectionRecord {
	r.Injection.StartedAt = at.Add(time.Hour)
	return r
}

// Capturing during the fault, with the start record, is the normal case.
func TestCaptureWithStartRecord(t *testing.T) {
	_, err := Capture(context.Background(), sources(t, false), Request{
		Scenario: scenario(t, "pending-sectors-postgres"), Variant: schema.VariantNPDAbsent,
		Node: "worker-02", Disk: "sacrificial", Inventory: inv(t),
		Injection: record("disk.pending_sectors", "real", false), OutDir: t.TempDir(),
	})
	if err != nil {
		t.Errorf("capture during an active fault: %v", err)
	}
}
