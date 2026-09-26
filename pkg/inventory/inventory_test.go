package inventory

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The committed example must always load: it is the documentation.
func TestExampleLoads(t *testing.T) {
	inv, err := Load(filepath.Join("..", "..", "hack", "inventory.example.yaml"))
	if err != nil {
		t.Fatalf("example inventory: %v", err)
	}
	if len(inv.Nodes) != 3 {
		t.Errorf("nodes = %d", len(inv.Nodes))
	}
	_, d, err := inv.InjectionTarget("worker-02", "sacrificial")
	if err != nil {
		t.Fatalf("sacrificial disk should be a valid target: %v", err)
	}
	if d.Target() != "/dev/disk/by-id/ata-EXAMPLE_HDD_2TB_SERIAL0001" {
		t.Errorf("target should prefer byId, got %s", d.Target())
	}
}

const minimal = `
apiVersion: tropis.io/v1alpha1
kind: Inventory
cluster: {name: lab, kubernetesVersion: v1.31.2}
nodes:
  - name: w1
    role: worker
    disks:
      - {id: system, device: /dev/sda}
      - {id: scratch, device: /dev/loop7, transport: loop, destructible: true}
`

// Injection must refuse every disk not explicitly marked destructible —
// including one where the field is simply absent.
func TestInjectionTargetRefusesProtectedDisks(t *testing.T) {
	inv, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inv.InjectionTarget("w1", "system"); !errors.Is(err, ErrNotDestructible) {
		t.Errorf("disk without destructible: err = %v, want ErrNotDestructible", err)
	}
	if _, d, err := inv.InjectionTarget("w1", "scratch"); err != nil || d.Target() != "/dev/loop7" {
		t.Errorf("destructible loop disk: %v, %v", d, err)
	}
	if _, _, err := inv.InjectionTarget("w9", "scratch"); err == nil {
		t.Error("unknown node should be an error")
	}
	if _, _, err := inv.InjectionTarget("w1", "nope"); err == nil {
		t.Error("unknown disk should be an error")
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name, doc, want string
	}{
		{"misspelt destructible", strings.Replace(minimal, "destructible: true", "destructable: true", 1), "destructable"},
		{"wrong apiVersion", strings.Replace(minimal, "tropis.io/v1alpha1", "v1", 1), "apiVersion"},
		{"no nodes", "apiVersion: tropis.io/v1alpha1\nkind: Inventory\ncluster: {name: lab, kubernetesVersion: v1}\nnodes: []", "at least one node"},
		{"bad role", strings.Replace(minimal, "role: worker", "role: master", 1), "role"},
		{"duplicate disk id", strings.Replace(minimal, "id: scratch", "id: system", 1), "used twice"},
		{"non-/dev device", strings.Replace(minimal, "device: /dev/loop7", "device: sdb", 1), "/dev path"},
		{"bad byId", strings.Replace(minimal, "device: /dev/loop7,", "device: /dev/loop7, byId: /dev/sdz,", 1), "by-id"},
		{"destructible at a system path", strings.Replace(minimal, "destructible: true}", "destructible: true, mountpoint: /var/lib/kubelet}", 1), "system path"},
		{"destructible at root", strings.Replace(minimal, "destructible: true}", "destructible: true, mountpoint: /}", 1), "system path"},
		{"bad transport", strings.Replace(minimal, "transport: loop", "transport: usb", 1), "transport"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
		})
	}
}

// Every problem is reported at once.
func TestValidateReportsAll(t *testing.T) {
	_, err := Parse([]byte("apiVersion: x\nkind: y\ncluster: {}\nnodes: []"))
	if err == nil || strings.Count(err.Error(), "\n  - ") < 4 {
		t.Errorf("want several problems listed, got %v", err)
	}
}
