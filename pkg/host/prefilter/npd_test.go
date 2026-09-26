package prefilter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nathanwebb/tropis/pkg/host/smart"
)

func TestNPDExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		res      Result
		wantCode int
	}{
		{"nothing found", Result{}, NPDOK},
		{"problem", Result{Findings: []Finding{{RuleID: RulePendingSectors, Device: "/dev/sda", Summary: "pending sectors 8"}}}, NPDProblem},
		{"unreadable only", Result{Unknown: []string{"sda (permission_denied)"}}, NPDUnknown},
		{
			// One failing disk and one unreadable disk: the node has a problem.
			"problem beats unknown",
			Result{
				Findings: []Finding{{RuleID: RuleHealthFailed, Device: "/dev/sda", Summary: "overall health FAILED"}},
				Unknown:  []string{"sdb (timeout)"},
			},
			NPDProblem,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := tt.res.NPD()
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if msg == "" {
				t.Error("NPD message must not be empty")
			}
			if len(msg) > NPDMaxOutput {
				t.Errorf("message is %d bytes, over the %d budget: %q", len(msg), NPDMaxOutput, msg)
			}
		})
	}
}

// Many findings across many devices must still fit, with the most decisive
// signal first so it survives truncation.
func TestNPDMessageTruncation(t *testing.T) {
	var fs []Finding
	for _, dev := range []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"} {
		fs = append(fs,
			Finding{RuleID: RuleHealthFailed, Device: dev, Summary: "overall health FAILED"},
			Finding{RuleID: RulePendingSectors, Device: dev, Summary: "pending sectors 168"},
		)
	}
	_, msg := Result{Findings: fs}.NPD()
	if len(msg) > NPDMaxOutput {
		t.Fatalf("message is %d bytes: %q", len(msg), msg)
	}
	if !strings.HasPrefix(msg, "sda: overall health FAILED") {
		t.Errorf("most decisive signal should lead: %q", msg)
	}
	if !strings.HasSuffix(msg, "...") {
		t.Errorf("truncated message should say so: %q", msg)
	}
}

func TestNPDMessageGroupsByDevice(t *testing.T) {
	_, msg := Result{Findings: []Finding{
		{RuleID: RuleHealthFailed, Device: "/dev/sdb", Summary: "overall health FAILED"},
		{RuleID: RulePendingSectors, Device: "/dev/sdb", Summary: "pending sectors 168"},
	}}.NPD()
	if msg != "sdb: overall health FAILED, pending sectors 168" {
		t.Errorf("message = %q", msg)
	}
}

func TestEvaluateReport(t *testing.T) {
	report := &smart.Report{
		Devices: []smart.Device{*healthySATA(), func() smart.Device {
			d := healthySATA()
			d.Path = "/dev/sdb"
			d.SerialNumber = "SN-B"
			d.ReallocatedSectors = u(30)
			return *d
		}()},
		Failures: []smart.DeviceFailure{{Path: "/dev/sdc", Reason: smart.FailurePermission}},
	}

	prevB := healthySATA()
	prevB.Path = "/dev/sdb"
	prevB.SerialNumber = "SN-B"
	prevB.ReallocatedSectors = u(10)
	previous := map[string]*smart.Device{DeviceKey(prevB): prevB}

	res := Evaluate(report, previous, DefaultThresholds())

	if got := ruleIDs(res.Findings); !reflect.DeepEqual(got, []string{RuleReallocatedGrowth}) {
		t.Errorf("findings = %v", got)
	}
	if len(res.Unknown) != 1 || !strings.Contains(res.Unknown[0], "permission_denied") {
		t.Errorf("unknown = %v", res.Unknown)
	}
	if !res.Candidate() {
		t.Error("node should be a candidate")
	}
	if got := res.TriggeredBy(); !reflect.DeepEqual(got, []string{RuleReallocatedGrowth}) {
		t.Errorf("triggeredBy = %v", got)
	}
}

// A drive replaced under the same path must not be compared against its
// predecessor.
func TestDeviceKeyUsesSerialNotPath(t *testing.T) {
	a := healthySATA()
	b := healthySATA()
	b.SerialNumber = "DIFFERENT"
	if DeviceKey(a) == DeviceKey(b) {
		t.Error("two drives at the same path with different serials must have different keys")
	}

	noSerial := healthySATA()
	noSerial.SerialNumber = ""
	if DeviceKey(noSerial) != "path:/dev/sda" {
		t.Errorf("key without serial = %q", DeviceKey(noSerial))
	}
}

// NPD is optional. Its absence must change nothing.
func TestNPDAbsentChangesNothing(t *testing.T) {
	report := &smart.Report{Devices: []smart.Device{*healthySATA()}}

	without := Evaluate(report, nil, DefaultThresholds())
	with := Evaluate(report, nil, DefaultThresholds())
	with.ApplyNPD(nil, DefaultNPDTriggerTypes)

	if !reflect.DeepEqual(without, with) {
		t.Errorf("applying no NPD conditions changed the result:\n%+v\n%+v", without, with)
	}
	if with.Candidate() {
		t.Error("healthy node with no NPD should not be a candidate")
	}
}

func TestApplyNPD(t *testing.T) {
	var res Result
	res.ApplyNPD([]NPDCondition{
		{Type: "ReadonlyFilesystem", Status: "True", Reason: "FilesystemIsReadOnly"},
		{Type: "KernelDeadlock", Status: "False"},
		{Type: "SomethingUnrelated", Status: "True"},
	}, DefaultNPDTriggerTypes)

	if !reflect.DeepEqual(res.NPDTriggers, []string{"npd.ReadonlyFilesystem"}) {
		t.Errorf("npdTriggers = %v", res.NPDTriggers)
	}
	if !res.Candidate() {
		t.Error("an NPD trigger alone should make the node a candidate")
	}
	// NPD triggers never masquerade as SMART findings.
	if len(res.Findings) != 0 {
		t.Errorf("NPD must not produce findings, got %v", res.Findings)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	prev, err := LoadState(dir)
	if err != nil || len(prev) != 0 {
		t.Fatalf("first run should load empty state, got %v, %v", prev, err)
	}

	report := &smart.Report{Devices: []smart.Device{*healthySATA()}}
	if err := SaveState(dir, prev, report); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	d, ok := got[DeviceKey(healthySATA())]
	if !ok {
		t.Fatalf("saved device missing from state: %v", got)
	}
	if *d.ReallocatedSectors != 0 {
		t.Errorf("reallocated = %d", *d.ReallocatedSectors)
	}
}

// A device unreadable on this sweep keeps its last good reading.
func TestSaveStateMergesAcrossSweeps(t *testing.T) {
	dir := t.TempDir()
	a := healthySATA()
	if err := SaveState(dir, nil, &smart.Report{Devices: []smart.Device{*a}}); err != nil {
		t.Fatal(err)
	}
	prev, _ := LoadState(dir)
	if err := SaveState(dir, prev, &smart.Report{}); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(dir)
	if _, ok := got[DeviceKey(a)]; !ok {
		t.Error("a device absent from this sweep should keep its previous reading")
	}
}

func TestLoadStateCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err == nil {
		t.Error("corrupt state should report an error for logging")
	}
	if state == nil {
		t.Error("corrupt state should still return a usable empty map")
	}
}
