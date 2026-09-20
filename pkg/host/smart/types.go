// Package smart invokes smartctl and normalises its JSON output.
//
// v1 collects SMART and nothing else. Kernel log, NIC, ECC and systemd
// collection are a later phase and deliberately absent here.
//
// The parser reads `smartctl -j`, whose JSON envelope is stable across
// vendors and device types even though the payload is not. SATA reports an
// attribute table of vendor-defined IDs; NVMe reports a fixed health log; SAS
// reports neither and gives error counters and a temperature. Device gives a
// single normalised view over all three, so rules do not branch on transport.
package smart

import (
	"fmt"
	"time"
)

// Transport is the device interface, which determines how SMART data is
// reported and therefore how it must be interpreted.
type Transport string

const (
	// TransportSATA covers ATA and SATA devices, reporting a vendor-defined
	// attribute table.
	TransportSATA Transport = "sata"

	// TransportNVMe devices report a fixed, standardised health information log.
	TransportNVMe Transport = "nvme"

	// TransportSAS covers SCSI and SAS devices, reporting error counter logs
	// rather than an attribute table.
	TransportSAS Transport = "sas"

	// TransportUnknown is a device whose transport could not be determined.
	TransportUnknown Transport = "unknown"
)

// Device is the normalised SMART state of one block device.
//
// It flattens the three reporting models onto one shape so that pre-filter
// rules and the reasoning layer do not have to branch on transport. Where a
// value has no equivalent on a given transport, the corresponding pointer is
// nil — distinguishing "not reported" from "reported as zero", a distinction
// that matters: a drive reporting zero reallocated sectors is healthy, while
// one not reporting the attribute at all tells us nothing.
type Device struct {
	// Path is the device node, for example "/dev/sda".
	Path string `json:"path"`

	// Transport determines how the underlying data was reported.
	Transport Transport `json:"transport"`

	// ModelName, SerialNumber and FirmwareVersion identify the drive.
	//
	// SerialNumber is redacted before reaching a model backend. It is retained
	// here because correlating a fault to a physical drive is the point of the
	// exercise for the operator reading the report.
	ModelName       string `json:"modelName,omitempty"`
	ModelFamily     string `json:"modelFamily,omitempty"`
	SerialNumber    string `json:"serialNumber,omitempty"`
	FirmwareVersion string `json:"firmwareVersion,omitempty"`

	// CapacityBytes is the user-addressable capacity. Zero when not reported.
	//
	// Parsed from smartctl's string form where available: the numeric form
	// overflows float64 precision on large devices, which is a real defect in
	// captured NVMe output rather than a theoretical concern.
	CapacityBytes uint64 `json:"capacityBytes,omitempty"`

	// RotationRate is 0 for SSDs, otherwise RPM. Nil when not reported.
	RotationRate *int `json:"rotationRate,omitempty"`

	// SMARTAvailable and SMARTEnabled report device support. A device can have
	// SMART available but disabled, in which case there is nothing to read and
	// nothing is wrong.
	SMARTAvailable bool `json:"smartAvailable"`
	SMARTEnabled   bool `json:"smartEnabled"`

	// HealthPassed is the device's own overall-health self-assessment. Nil
	// when the device did not report one.
	//
	// A false value is the strongest single signal SMART produces: the drive
	// is saying it expects to fail. It is rare, and rarely early.
	HealthPassed *bool `json:"healthPassed,omitempty"`

	// TemperatureCelsius is the current drive temperature. Nil when not reported.
	TemperatureCelsius *int `json:"temperatureCelsius,omitempty"`

	// PowerOnHours is drive lifetime. Nil when not reported. Used to judge
	// whether a defect count is alarming or merely consistent with age.
	PowerOnHours *uint64 `json:"powerOnHours,omitempty"`

	// PowerCycleCount is the number of power cycles. Nil when not reported.
	PowerCycleCount *uint64 `json:"powerCycleCount,omitempty"`

	// The normalised defect counters. Each is nil when the transport or device
	// does not report it.
	//
	// ReallocatedSectors is sectors the drive has remapped to spares. Growth
	// matters far more than the absolute value.
	ReallocatedSectors *uint64 `json:"reallocatedSectors,omitempty"`

	// PendingSectors are sectors the drive is having trouble reading and has
	// not yet remapped. Usually the earliest actionable signal.
	PendingSectors *uint64 `json:"pendingSectors,omitempty"`

	// UncorrectableErrors is uncorrectable read/write errors.
	UncorrectableErrors *uint64 `json:"uncorrectableErrors,omitempty"`

	// MediaErrors is NVMe's media and data integrity error count.
	MediaErrors *uint64 `json:"mediaErrors,omitempty"`

	// PercentageUsed is NVMe's wear indicator, where 100 means the rated
	// endurance has been consumed. It may exceed 100.
	PercentageUsed *uint64 `json:"percentageUsed,omitempty"`

	// AvailableSpare and AvailableSpareThreshold are NVMe spare capacity, as
	// percentages. Spare falling below the threshold is a defined failure
	// condition.
	AvailableSpare          *uint64 `json:"availableSpare,omitempty"`
	AvailableSpareThreshold *uint64 `json:"availableSpareThreshold,omitempty"`

	// CriticalWarning is NVMe's critical warning bitfield. Non-zero means the
	// controller is reporting a specific failure condition.
	CriticalWarning *uint64 `json:"criticalWarning,omitempty"`

	// Attributes holds the SATA attribute table verbatim, preserving vendor
	// semantics the normalised fields cannot express. Empty on other
	// transports.
	Attributes []Attribute `json:"attributes,omitempty"`

	// SelfTestErrors is the count of logged self-test failures.
	SelfTestErrors *uint64 `json:"selfTestErrors,omitempty"`

	// ErrorLogCount is the total entries in the device's error log.
	ErrorLogCount *uint64 `json:"errorLogCount,omitempty"`

	// CollectedAt is when this reading was taken.
	CollectedAt time.Time `json:"collectedAt"`

	// SmartctlVersion is the smartctl version that produced the reading,
	// recorded because output shape varies across releases.
	SmartctlVersion string `json:"smartctlVersion,omitempty"`

	// Messages carries any warnings or errors smartctl emitted alongside a
	// partially successful reading.
	Messages []Message `json:"messages,omitempty"`
}

// Attribute is one entry from a SATA SMART attribute table.
//
// Retained in full because vendors define IDs above 100 inconsistently, and
// the raw string sometimes encodes several values in one attribute — the
// Seagate Command_Timeout "4 4 3" form, for instance, where the numeric raw
// value is a packed field rather than a count. Rules use the normalised
// counters; the reasoning layer sees these.
type Attribute struct {
	// ID is the attribute ID, 1-255.
	ID int `json:"id"`

	// Name is smartctl's name for the attribute, resolved against its drive
	// database where the drive is known.
	Name string `json:"name"`

	// Value is the current normalised value, typically 1-253, where higher is
	// healthier.
	Value int `json:"value"`

	// Worst is the worst normalised value recorded.
	Worst int `json:"worst"`

	// Threshold is the value at which the drive considers the attribute
	// failed.
	Threshold int `json:"threshold"`

	// WhenFailed is "now", "past", or "" — whether the normalised value has
	// ever fallen below the threshold.
	WhenFailed string `json:"whenFailed,omitempty"`

	// RawValue is the raw counter as an integer.
	RawValue uint64 `json:"rawValue"`

	// RawString is smartctl's formatted rendering of the raw value, which may
	// differ materially from RawValue for packed attributes.
	RawString string `json:"rawString,omitempty"`

	// Prefailure marks an attribute the drive treats as predicting failure,
	// as opposed to a lifetime statistic.
	Prefailure bool `json:"prefailure,omitempty"`
}

// Failed reports whether this attribute is currently below its threshold.
func (a Attribute) Failed() bool {
	return a.WhenFailed == "now"
}

// Message is a diagnostic emitted by smartctl.
type Message struct {
	// Severity is typically "error" or "warning".
	Severity string `json:"severity"`
	// Text is the message.
	Text string `json:"text"`
}

// Report is the result of one collection sweep over a host's devices.
type Report struct {
	// Devices holds a successfully parsed reading per device.
	Devices []Device `json:"devices"`

	// Failures records devices that could not be read, and why. A device that
	// cannot be read is not the same as a healthy device, and the distinction
	// must survive to the reasoning layer: "the disk could not be queried" is
	// itself a finding.
	Failures []DeviceFailure `json:"failures,omitempty"`

	// CollectedAt is when the sweep ran.
	CollectedAt time.Time `json:"collectedAt"`
}

// DeviceFailure records a device that could not be read.
type DeviceFailure struct {
	// Path is the device that failed.
	Path string `json:"path"`
	// Reason classifies the failure.
	Reason FailureReason `json:"reason"`
	// Detail is the underlying message from smartctl or the OS.
	Detail string `json:"detail,omitempty"`
}

func (f DeviceFailure) String() string {
	return fmt.Sprintf("%s: %s (%s)", f.Path, f.Reason, f.Detail)
}

// FailureReason classifies why a device could not be read.
//
// These are distinguished because they call for different responses: a
// permission failure is a deployment problem, a missing device may be a
// removed disk, and an unsupported device is simply not interesting.
type FailureReason string

const (
	// FailureNotFound means the device node does not exist.
	FailureNotFound FailureReason = "device_not_found"

	// FailurePermission means smartctl could not open the device. Usually the
	// collector is missing SYS_RAWIO or device access.
	FailurePermission FailureReason = "permission_denied"

	// FailureUnsupported means the device does not support SMART, or support
	// is disabled.
	FailureUnsupported FailureReason = "smart_unsupported"

	// FailureSmartctlMissing means the smartctl binary is not installed.
	FailureSmartctlMissing FailureReason = "smartctl_not_found"

	// FailureParse means smartctl produced output that could not be parsed.
	FailureParse FailureReason = "parse_error"

	// FailureTimeout means smartctl did not return in time. A drive that hangs
	// on a SMART query is itself a signal.
	FailureTimeout FailureReason = "timeout"

	// FailureOther is any other failure.
	FailureOther FailureReason = "other"
)
