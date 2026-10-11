package capture

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/00Webbo/tropis/eval/scenarios"
	"github.com/00Webbo/tropis/pkg/schema"
)

// sampleSerial is the serial number in the sata-failing sample.
const sampleSerial = "Z4Z1KQPL"

// reading returns the sata-failing smartctl sample with its serial and its
// reallocated (5), pending (197) and uncorrectable (187) counts replaced, so
// tests build before/after readings from real smartctl output.
func reading(t *testing.T, serial string, realloc, pending, uncorr uint64) schema.RawJSON {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "host", "smart", "testdata", "sata-failing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["serial_number"] = serial
	set := map[float64]uint64{5: realloc, 197: pending, 187: uncorr}
	table := doc["ata_smart_attributes"].(map[string]any)["table"].([]any)
	for _, a := range table {
		attr := a.(map[string]any)
		if v, ok := set[attr["id"].(float64)]; ok {
			attr["raw"] = map[string]any{"value": v, "string": strconv.FormatUint(v, 10)}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return schema.RawJSON(out)
}

func TestCheckSMARTChange(t *testing.T) {
	pending := []string{scenarios.CounterPending}
	realloc := []string{scenarios.CounterReallocated}
	other := "WD-OTHER0001"
	type readings map[string]schema.RawJSON
	tests := []struct {
		name    string
		expect  []string
		serial  string
		before  readings // nil means no earlier reading at all
		after   readings
		refused string // "" means the check passes
	}{
		{"pending grew", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 0)}, ""},
		{"reallocated grew", realloc, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 8, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 12, 0, 0)}, ""},
		{"one of several expected counters grew", []string{scenarios.CounterReallocated, scenarios.CounterPending}, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 8, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 8, 4, 0)}, ""},
		{"no change", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)}, "pending_sectors 0 -> 0"},
		{"decrease", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 4, 0, 0)}, "pending_sectors 4 -> 0"},
		{"growth in a counter the scenario does not expect", realloc, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 9)}, "reallocated_sectors 0 -> 0"},
		{"growth on a different drive", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0), "/dev/sdc": reading(t, other, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0), "/dev/sdc": reading(t, other, 0, 4, 0)}, "pending_sectors 0 -> 0"},
		{"different drive at the target's old path", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, other, 0, 4, 0), "/dev/sdc": reading(t, sampleSerial, 0, 0, 0)}, "pending_sectors 0 -> 0"},
		{"matched by serial when the path changes", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdc": reading(t, sampleSerial, 0, 4, 0)}, ""},
		{"target drive missing from the capture", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdc": reading(t, other, 0, 4, 0)}, "no SMART reading of that drive"},
		{"target drive missing from the earlier reading", pending, sampleSerial,
			readings{"/dev/sdc": reading(t, other, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 0)}, "does not include that drive"},
		{"no earlier reading", pending, sampleSerial,
			nil,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 0)}, "no earlier reading"},
		{"serial unknown", pending, "",
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 0)}, "serial"},
		{"two devices with the target's serial", pending, sampleSerial,
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 0, 0)},
			readings{"/dev/sdb": reading(t, sampleSerial, 0, 4, 0), "/dev/sdc": reading(t, sampleSerial, 0, 4, 0)}, "two devices"},
		{"no SMART effect expected", nil, "",
			nil, readings{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := &schema.HostCapture{SMART: tt.after, CollectedAt: at}
			if tt.before != nil {
				hc.Previous = &schema.HostSnapshot{SMART: tt.before, CollectedAt: at.Add(-time.Hour)}
			}
			err := CheckSMARTChange("some-scenario", tt.expect, tt.serial, hc)
			if tt.refused == "" {
				if err != nil {
					t.Fatalf("expected the check to pass: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected the capture to be refused")
			}
			if !errors.Is(err, ErrRefused) {
				t.Errorf("not a refusal: %v", err)
			}
			if !strings.Contains(err.Error(), tt.refused) || !strings.Contains(err.Error(), "some-scenario") {
				t.Errorf("error %q should name the scenario and mention %q", err, tt.refused)
			}
		})
	}
}

func TestSerialFromRecord(t *testing.T) {
	rec := func(params map[string]string) *InjectionRecord {
		return &InjectionRecord{Injection: &schema.Injection{Params: params}}
	}
	tests := []struct {
		rec  *InjectionRecord
		want string
	}{
		{rec(map[string]string{"smartBefore": "reallocated=0 pending=0 uncorrectable=0 serial=Z4Z1KQPL"}), "Z4Z1KQPL"},
		{rec(map[string]string{"smartAfter": "reallocated=0 pending=0 uncorrectable=0 serial=WD WCC4 123"}), "WD WCC4 123"},
		{rec(map[string]string{"smartBefore": "reallocated=0 pending=0 uncorrectable=0"}), ""},
		{rec(map[string]string{"smartBefore": "unavailable"}), ""},
		{rec(map[string]string{"smartBefore": "reallocated=0 pending=0 uncorrectable=0 serial="}), ""},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := SerialFromRecord(tt.rec); got != tt.want {
			t.Errorf("SerialFromRecord(%v) = %q, want %q", tt.rec, got, tt.want)
		}
	}
}

// Through Capture: a real-sata scenario whose drive did not react writes
// nothing, and a scenario without a SMART expectation is not checked.
func TestCaptureChecksSMARTChange(t *testing.T) {
	unchanged := reading(t, sampleSerial, 1544, 168, 96)
	still := Sources{
		Host: fakeHost{cur: map[string]schema.RawJSON{"/dev/sdb": unchanged}, prev: map[string]schema.RawJSON{"/dev/sdb": unchanged}},
		K8s:  fakeK8s{}, Now: func() time.Time { return at },
	}
	noHistory := Sources{
		Host: fakeHost{cur: map[string]schema.RawJSON{"/dev/sdb": unchanged}},
		K8s:  fakeK8s{}, Now: func() time.Time { return at },
	}
	noSerial := record("disk.pending_sectors", "real", false)
	delete(noSerial.Injection.Params, "smartBefore")

	tests := []struct {
		name     string
		src      Sources
		scenario string
		inj      *InjectionRecord
		want     string // "" means the capture succeeds
	}{
		{"pending grew", sources(t, false), "pending-sectors-postgres", record("disk.pending_sectors", "real", false), ""},
		{"reallocated grew", sources(t, false), "degradation-minio", record("disk.progressive_degradation", "real", false), ""},
		{"pending did not move", still, "pending-sectors-postgres", record("disk.pending_sectors", "real", false),
			"pending-sectors-postgres expects pending_sectors to grow on drive Z4Z1KQPL (/dev/sdb)"},
		{"reallocated did not move", still, "degradation-minio", record("disk.progressive_degradation", "real", false),
			"reallocated_sectors 1544 -> 1544"},
		{"record without the drive's serial", sources(t, false), "pending-sectors-postgres", noSerial, "serial"},
		{"no SMART expectation, no history", noHistory, "read-errors-postgres", record("disk.read_errors", "none", false), ""},
		{"no injection at all", still, "stable-defects-config-crash", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := t.TempDir()
			_, err := Capture(context.Background(), tt.src, Request{
				Scenario: scenario(t, tt.scenario), Variant: schema.VariantNPDAbsent, Node: "worker-02", Disk: "sacrificial",
				Inventory: inv(t), Injection: tt.inj, OutDir: out,
			})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Capture: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v should be a refusal mentioning %q", err, tt.want)
			}
			if entries, _ := os.ReadDir(out); len(entries) != 0 {
				t.Error("a refused capture must write nothing")
			}
		})
	}
}
