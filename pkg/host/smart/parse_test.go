package smart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read sample %s: %v", name, err)
	}
	return data
}

func mustParse(t *testing.T, name, path string) *Device {
	t.Helper()
	dev, fail := Parse(readSample(t, name), path)
	if fail != nil {
		t.Fatalf("Parse(%s) failed unexpectedly: %v", name, fail)
	}
	if dev == nil {
		t.Fatalf("Parse(%s) returned no device and no failure", name)
	}
	return dev
}

func TestParseSATAHealthy(t *testing.T) {
	dev := mustParse(t, "sata-healthy.json", "/dev/sda")

	if dev.Transport != TransportSATA {
		t.Errorf("transport = %q, want %q", dev.Transport, TransportSATA)
	}
	if dev.ModelName != "Samsung SSD 870 EVO 1TB" {
		t.Errorf("modelName = %q", dev.ModelName)
	}
	if dev.SerialNumber != "S5Y2NG0R123456K" {
		t.Errorf("serialNumber = %q", dev.SerialNumber)
	}
	if !dev.SMARTAvailable || !dev.SMARTEnabled {
		t.Errorf("SMART support = available:%v enabled:%v, want both true",
			dev.SMARTAvailable, dev.SMARTEnabled)
	}
	if dev.HealthPassed == nil || !*dev.HealthPassed {
		t.Errorf("healthPassed = %v, want true", dev.HealthPassed)
	}

	// A healthy drive reports zero defects. Zero must be distinguishable from
	// "not reported", so these are non-nil pointers to zero.
	assertUint(t, "reallocatedSectors", dev.ReallocatedSectors, 0)
	assertUint(t, "pendingSectors", dev.PendingSectors, 0)
	assertUint(t, "uncorrectableErrors", dev.UncorrectableErrors, 0)
	assertUint(t, "powerOnHours", dev.PowerOnHours, 4821)
	assertUint(t, "powerCycleCount", dev.PowerCycleCount, 47)

	if dev.TemperatureCelsius == nil || *dev.TemperatureCelsius != 32 {
		t.Errorf("temperature = %v, want 32", dev.TemperatureCelsius)
	}
	if dev.RotationRate == nil || *dev.RotationRate != 0 {
		t.Errorf("rotationRate = %v, want 0 (SSD)", dev.RotationRate)
	}
	if dev.CapacityBytes != 1000204886016 {
		t.Errorf("capacityBytes = %d", dev.CapacityBytes)
	}
	if len(dev.Attributes) != 10 {
		t.Errorf("parsed %d attributes, want 10", len(dev.Attributes))
	}
}

func TestParseSATAFailing(t *testing.T) {
	dev := mustParse(t, "sata-failing.json", "/dev/sdb")

	if dev.Transport != TransportSATA {
		t.Errorf("transport = %q", dev.Transport)
	}

	// The drive's own assessment is the strongest single SMART signal.
	if dev.HealthPassed == nil || *dev.HealthPassed {
		t.Errorf("healthPassed = %v, want false for a failing drive", dev.HealthPassed)
	}

	assertUint(t, "reallocatedSectors", dev.ReallocatedSectors, 1544)
	assertUint(t, "pendingSectors", dev.PendingSectors, 168)
	assertUint(t, "powerOnHours", dev.PowerOnHours, 39744)

	// 187 Reported_Uncorrect is preferred over 198 Offline_Uncorrectable when
	// both are present, since it counts errors reported to the host.
	assertUint(t, "uncorrectableErrors", dev.UncorrectableErrors, 412)

	assertUint(t, "errorLogCount", dev.ErrorLogCount, 412)
	assertUint(t, "selfTestErrors", dev.SelfTestErrors, 1)

	if dev.RotationRate == nil || *dev.RotationRate != 7200 {
		t.Errorf("rotationRate = %v, want 7200", dev.RotationRate)
	}

	// An attribute that failed in the past must be visible as such.
	var temp *Attribute
	for i := range dev.Attributes {
		if dev.Attributes[i].ID == 190 {
			temp = &dev.Attributes[i]
		}
	}
	if temp == nil {
		t.Fatal("attribute 190 missing")
	}
	if temp.WhenFailed != "past" {
		t.Errorf("attribute 190 whenFailed = %q, want past", temp.WhenFailed)
	}
	if temp.Failed() {
		t.Error("attribute 190 failed in the past, so Failed() (which means now) should be false")
	}
}

// Packed raw attributes must keep their formatted string: the integer is a
// packed field rather than a count, and discarding the string would lose the
// only interpretable form.
func TestParseRetainsPackedRawString(t *testing.T) {
	dev := mustParse(t, "sata-failing.json", "/dev/sdb")

	for _, a := range dev.Attributes {
		if a.ID != 188 {
			continue
		}
		if a.RawString != "4 4 3" {
			t.Errorf("attribute 188 rawString = %q, want %q", a.RawString, "4 4 3")
		}
		if a.RawValue != 17180131331 {
			t.Errorf("attribute 188 rawValue = %d", a.RawValue)
		}
		return
	}
	t.Fatal("attribute 188 missing")
}

func TestParseNVMe(t *testing.T) {
	dev := mustParse(t, "nvme-healthy.json", "/dev/nvme0n1")

	if dev.Transport != TransportNVMe {
		t.Errorf("transport = %q, want %q", dev.Transport, TransportNVMe)
	}
	if dev.ModelName != "Linux" {
		t.Errorf("modelName = %q", dev.ModelName)
	}
	if dev.HealthPassed == nil || !*dev.HealthPassed {
		t.Errorf("healthPassed = %v, want true", dev.HealthPassed)
	}

	assertUint(t, "percentageUsed", dev.PercentageUsed, 0)
	assertUint(t, "mediaErrors", dev.MediaErrors, 0)
	assertUint(t, "criticalWarning", dev.CriticalWarning, 0)
	assertUint(t, "availableSpare", dev.AvailableSpare, 0)

	// Media errors stand in for uncorrectable errors on NVMe, so
	// transport-independent rules have something to read.
	assertUint(t, "uncorrectableErrors", dev.UncorrectableErrors, 0)

	// NVMe has no reallocated or pending sector concept at all. Nil, not zero:
	// the drive is not reporting zero, it has nothing to report.
	if dev.ReallocatedSectors != nil {
		t.Errorf("reallocatedSectors = %v, want nil on NVMe", *dev.ReallocatedSectors)
	}
	if dev.PendingSectors != nil {
		t.Errorf("pendingSectors = %v, want nil on NVMe", *dev.PendingSectors)
	}
	if len(dev.Attributes) != 0 {
		t.Errorf("NVMe should have no ATA attribute table, got %d", len(dev.Attributes))
	}
}

// Real NVMe output reports a capacity exceeding float64 integer precision.
// The exact string form must win over the lossy number.
func TestParseNVMeCapacityPrecision(t *testing.T) {
	dev := mustParse(t, "nvme-healthy.json", "/dev/nvme0n1")

	// The sample reports 9903520314283042199729864704, which exceeds uint64.
	// Parsing must not panic, and must not produce a wildly wrong small value.
	if dev.CapacityBytes != 0 && dev.CapacityBytes < 1<<40 {
		t.Errorf("capacityBytes = %d, implausible for the sample", dev.CapacityBytes)
	}
}

func TestParseSCSI(t *testing.T) {
	dev := mustParse(t, "scsi-sas-healthy.json", "/dev/sdf")

	if dev.Transport != TransportSAS {
		t.Errorf("transport = %q, want %q", dev.Transport, TransportSAS)
	}
	if dev.ModelName != "Linux scsi_debug" {
		t.Errorf("modelName = %q", dev.ModelName)
	}

	// SMART available but not enabled is a real and unremarkable state.
	if !dev.SMARTAvailable {
		t.Error("smartAvailable = false, want true")
	}
	if dev.SMARTEnabled {
		t.Error("smartEnabled = true, want false for this sample")
	}
	if dev.TemperatureCelsius == nil || *dev.TemperatureCelsius != 38 {
		t.Errorf("temperature = %v, want 38", dev.TemperatureCelsius)
	}
	if len(dev.Attributes) != 0 {
		t.Errorf("SCSI has no ATA attribute table, got %d", len(dev.Attributes))
	}
}

// A device present but reporting nothing must parse cleanly rather than
// failing: there is no fault here, just no data.
func TestParseDeviceWithoutSMART(t *testing.T) {
	dev, fail := Parse(readSample(t, "scsi-no-smart.json"), "/dev/sda")
	if fail != nil {
		t.Fatalf("a device reporting no SMART data is not a failure, got: %v", fail)
	}
	if dev.HealthPassed != nil {
		t.Errorf("healthPassed = %v, want nil when the device reported none", *dev.HealthPassed)
	}
	if dev.SMARTAvailable {
		t.Error("smartAvailable should be false")
	}
}

// The failure paths, all keyed on the JSON envelope rather than the process
// exit code. smartctl emits valid JSON describing each of these.
func TestParseFailures(t *testing.T) {
	tests := []struct {
		name       string
		sample     string
		path       string
		wantReason FailureReason
	}{
		{
			name:       "missing device",
			sample:     "error-missing-device.json",
			path:       "/dev/nonexistent0",
			wantReason: FailureNotFound,
		},
		{
			name:       "permission denied",
			sample:     "error-permission-denied.json",
			path:       "/dev/sda",
			wantReason: FailurePermission,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev, fail := Parse(readSample(t, tt.sample), tt.path)
			if fail == nil {
				t.Fatalf("expected a failure, got device %+v", dev)
			}
			if dev != nil {
				t.Error("a failure must not also return a device")
			}
			if fail.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q (detail: %s)",
					fail.Reason, tt.wantReason, fail.Detail)
			}
			if fail.Detail == "" {
				t.Error("failure detail should carry smartctl's message")
			}
			if fail.Path == "" {
				t.Error("failure should name the device")
			}
		})
	}
}

func TestParseMalformedJSON(t *testing.T) {
	dev, fail := Parse([]byte("{this is not json"), "/dev/sda")
	if fail == nil {
		t.Fatalf("expected a parse failure, got %+v", dev)
	}
	if fail.Reason != FailureParse {
		t.Errorf("reason = %q, want %q", fail.Reason, FailureParse)
	}
	if fail.Path != "/dev/sda" {
		t.Errorf("path = %q, want the requested device", fail.Path)
	}
}

// Empty output is a distinct case from malformed output and must not panic.
func TestParseEmptyOutput(t *testing.T) {
	if _, fail := Parse(nil, "/dev/sda"); fail == nil {
		t.Error("expected a failure for empty output")
	}
	if _, fail := Parse([]byte("{}"), "/dev/sda"); fail != nil {
		// An empty JSON object has exit_status 0 and no device data. It is
		// not a failure, just an empty reading.
		t.Errorf("empty JSON object should parse as an empty reading, got %v", fail)
	}
}

func TestClassifyTransport(t *testing.T) {
	tests := []struct {
		devType  string
		protocol string
		want     Transport
	}{
		{"sat", "ATA", TransportSATA},
		{"ata", "ATA", TransportSATA},
		{"nvme", "NVMe", TransportNVMe},
		{"scsi", "SCSI", TransportSAS},
		{"sas", "SCSI", TransportSAS},
		{"", "ATA", TransportSATA},
		{"", "NVMe", TransportNVMe},
		{"", "SCSI", TransportSAS},
		{"", "", TransportUnknown},
		{"weird", "", TransportUnknown},
	}

	for _, tt := range tests {
		if got := classifyTransport(tt.devType, tt.protocol); got != tt.want {
			t.Errorf("classifyTransport(%q, %q) = %q, want %q",
				tt.devType, tt.protocol, got, tt.want)
		}
	}
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		detail string
		want   FailureReason
	}{
		{"Smartctl open device: /dev/sda failed: Permission denied", FailurePermission},
		{"/dev/nonexistent0: Unable to detect device type", FailureNotFound},
		{"Smartctl open device: /dev/sdz failed: No such file or directory", FailureNotFound},
		{"Device does not support SMART", FailureUnsupported},
		{"Read NVMe Identify Controller failed: Invalid argument", FailureOther},
		{"", FailureOther},
	}

	for _, tt := range tests {
		if got := classifyFailure(tt.detail); got != tt.want {
			t.Errorf("classifyFailure(%q) = %q, want %q", tt.detail, got, tt.want)
		}
	}
}

// The exit status is a bitfield, and misreading it is the easiest way to get
// this collector wrong. Bits 3-7 are findings about a drive that was read
// perfectly well — a collector treating "exit != 0" as an error would discard
// exactly the readings that matter most.
func TestExitStatusFindingsAreNotFailures(t *testing.T) {
	// The failing SATA sample carries exit_status 24 (bits 3 and 4: disk
	// failing, prefail attribute at or below threshold).
	dev, fail := Parse(readSample(t, "sata-failing.json"), "/dev/sdb")
	if fail != nil {
		t.Fatalf("exit_status 24 reports drive findings, not a read failure; got %v", fail)
	}
	if dev.HealthPassed == nil || *dev.HealthPassed {
		t.Error("the failing sample should parse as a failed health assessment")
	}
}

// Bit 2 means "some SMART command failed", which real devices set while still
// returning complete, usable data. Both captured SCSI samples do exactly this.
// Treating bit 2 as fatal would discard every such reading.
func TestExitStatusPartialReadIsKept(t *testing.T) {
	// scsi-sas-healthy.json carries exit_status 4 with a full device block,
	// temperature and health status.
	dev, fail := Parse(readSample(t, "scsi-sas-healthy.json"), "/dev/sdf")
	if fail != nil {
		t.Fatalf("exit_status 4 with usable data is a partial read, not a failure; got %v", fail)
	}
	if dev.TemperatureCelsius == nil {
		t.Error("the partial reading still carried a temperature and should keep it")
	}

	// The partial read is recorded so a missing field downstream is explicable.
	var warned bool
	for _, m := range dev.Messages {
		if m.Severity == "warning" && strings.Contains(m.Text, "partial read") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a partial read should be recorded in Messages, got %+v", dev.Messages)
	}
}

// Bit 2 with no device identified at all is a genuine failure: the command
// failed before producing anything usable.
func TestExitStatusCommandFailedWithoutDevice(t *testing.T) {
	// A synthetic envelope: bit 2 set, no device block.
	out := `{"smartctl":{"exit_status":4,"messages":[{"string":"Operation not supported","severity":"error"}]}}`

	dev, fail := Parse([]byte(out), "/dev/sdx")
	if fail == nil {
		t.Fatalf("expected a failure when no device was identified, got %+v", dev)
	}
	if fail.Reason != FailureUnsupported {
		t.Errorf("reason = %q, want %q", fail.Reason, FailureUnsupported)
	}
}

// The wrong-device-type sample identifies the device but fails the NVMe
// command against it, so it is a partial read of a known device.
func TestParseWrongDeviceType(t *testing.T) {
	dev, fail := Parse(readSample(t, "error-wrong-devtype.json"), "/dev/sda")
	if fail != nil {
		t.Fatalf("the device was identified, so this is a partial read; got %v", fail)
	}
	if dev.Path != "/dev/sda" {
		t.Errorf("path = %q", dev.Path)
	}
	// smartctl's error message is preserved for the operator.
	var sawError bool
	for _, m := range dev.Messages {
		if m.Severity == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Error("smartctl's error message should be preserved in Messages")
	}
}

func assertUint(t *testing.T, name string, got *uint64, want uint64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %d", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %d, want %d", name, *got, want)
	}
}
