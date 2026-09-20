package smart

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// smartctl exit status is a bitfield, not an error code, and reading it as
// one is the single easiest way to get this collector wrong. Bits 3-7 are set
// on drives that were read perfectly well — they are findings about the disk,
// and are exactly what Tropis is looking for. A collector treating "exit != 0"
// as an error would discard precisely the readings that matter most.
//
// Bit meanings are as documented in smartctl(8):
const (
	exitCommandLineError = 1 << 0 // command line did not parse
	exitDeviceOpenFailed = 1 << 1 // device open failed, or no IDENTIFY DEVICE
	exitCommandFailed    = 1 << 2 // some SMART/ATA command failed, or a checksum error

	exitDiskFailing   = 1 << 3 // SMART status check returned DISK FAILING
	exitPrefailBelow  = 1 << 4 // prefail attributes at or below threshold
	exitPrefailPast   = 1 << 5 // attributes were at or below threshold in the past
	exitErrorsLogged  = 1 << 6 // the device error log contains records
	exitSelfTestError = 1 << 7 // the self-test log contains records of errors
)

// unreadableMask are the bits meaning no usable reading was produced at all.
//
// Only bits 0 and 1 qualify. Bit 2 means *some* command failed, which real
// devices set while still returning complete, usable data: the captured
// scsi_debug sample exits 4 and carries a full device block, temperature and
// health status. Treating bit 2 as fatal would discard every such reading,
// and SCSI devices commonly set it for an unsupported optional log page.
//
// Bit 2 alone is therefore recorded as a partial-read warning, not a failure;
// callers see it in Device.Messages.
const unreadableMask = exitCommandLineError | exitDeviceOpenFailed

// smartctlOutput mirrors the parts of `smartctl -j` output Tropis reads.
//
// It is deliberately a partial mapping. smartctl emits a great deal more, and
// unknown fields are ignored rather than rejected: the schema grows between
// releases and across vendors, and a collector that failed on an unrecognised
// field would break on a drive it had never seen. The raw output is preserved
// in the fixture regardless, so nothing is lost.
type smartctlOutput struct {
	JSONFormatVersion []int `json:"json_format_version"`

	Smartctl struct {
		Version    []int `json:"version"`
		ExitStatus int   `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`

	LocalTime struct {
		TimeT int64 `json:"time_t"`
	} `json:"local_time"`

	Device struct {
		Name     string `json:"name"`
		InfoName string `json:"info_name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`

	ModelName       string `json:"model_name"`
	ModelFamily     string `json:"model_family"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`

	// SCSI devices report vendor and product separately.
	SCSIVendor    string `json:"scsi_vendor"`
	SCSIProduct   string `json:"scsi_product"`
	SCSIModelName string `json:"scsi_model_name"`

	UserCapacity struct {
		Blocks uint64 `json:"blocks"`
		// Bytes overflows float64 precision on large devices; BytesStr carries
		// the exact value and is preferred when present. Observed in real NVMe
		// captures, not a hypothetical.
		Bytes    json.Number `json:"bytes"`
		BytesStr string      `json:"bytes_s"`
	} `json:"user_capacity"`

	RotationRate *int `json:"rotation_rate"`

	SmartSupport struct {
		Available bool `json:"available"`
		Enabled   bool `json:"enabled"`
	} `json:"smart_support"`

	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`

	Temperature *struct {
		Current *int `json:"current"`
	} `json:"temperature"`

	PowerOnTime *struct {
		Hours *uint64 `json:"hours"`
	} `json:"power_on_time"`

	PowerCycleCount *uint64 `json:"power_cycle_count"`

	ATASmartAttributes *struct {
		Revision int `json:"revision"`
		Table    []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Flags      struct {
				Prefailure bool `json:"prefailure"`
			} `json:"flags"`
			Raw struct {
				Value  uint64 `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`

	NVMeHealthLog *struct {
		CriticalWarning         *uint64 `json:"critical_warning"`
		AvailableSpare          *uint64 `json:"available_spare"`
		AvailableSpareThreshold *uint64 `json:"available_spare_threshold"`
		PercentageUsed          *uint64 `json:"percentage_used"`
		PowerOnHours            *uint64 `json:"power_on_hours"`
		PowerCycles             *uint64 `json:"power_cycles"`
		MediaErrors             *uint64 `json:"media_errors"`
		NumErrLogEntries        *uint64 `json:"num_err_log_entries"`
		UnsafeShutdowns         *uint64 `json:"unsafe_shutdowns"`
	} `json:"nvme_smart_health_information_log"`

	ATAErrorLog *struct {
		Summary struct {
			Count uint64 `json:"count"`
		} `json:"summary"`
	} `json:"ata_smart_error_log"`

	ATASelfTestLog *struct {
		Standard struct {
			ErrorCountTotal uint64 `json:"error_count_total"`
		} `json:"standard"`
	} `json:"ata_smart_self_test_log"`

	// SCSI error counters, reported instead of an attribute table.
	SCSIGrownDefectList *uint64 `json:"scsi_grown_defect_list"`

	SCSIErrorCounterLog *struct {
		Read *struct {
			TotalUncorrectedErrors *uint64 `json:"total_uncorrected_errors"`
		} `json:"read"`
		Write *struct {
			TotalUncorrectedErrors *uint64 `json:"total_uncorrected_errors"`
		} `json:"write"`
	} `json:"scsi_error_counter_log"`
}

// Parse reads `smartctl -j` output into a normalised Device.
//
// It returns a DeviceFailure rather than a Device when smartctl reported that
// no usable reading was obtained. Crucially, the classification comes from the
// JSON envelope, not from the process exit code: smartctl emits valid JSON
// describing the problem even when it fails, and callers that inspect only the
// exit code lose that detail.
//
// The devicePath argument is used when the output carries no device name,
// which happens for a device that could not be opened at all.
func Parse(data []byte, devicePath string) (*Device, *DeviceFailure) {
	var out smartctlOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &DeviceFailure{
			Path:   devicePath,
			Reason: FailureParse,
			Detail: fmt.Sprintf("smartctl output is not valid JSON: %v", err),
		}
	}

	path := out.Device.Name
	if path == "" {
		path = devicePath
	}

	messages := make([]Message, 0, len(out.Smartctl.Messages))
	var errText []string
	for _, m := range out.Smartctl.Messages {
		messages = append(messages, Message{Severity: m.Severity, Text: m.String})
		if m.Severity == "error" {
			errText = append(errText, m.String)
		}
	}
	detail := strings.Join(errText, "; ")

	// Whether a reading is usable is decided by whether smartctl identified a
	// device, not by the exit status alone.
	//
	// The bits are genuinely ambiguous on their own. Bit 1 nominally means
	// "device open failed", but a captured virtual SCSI disk sets it while
	// reporting a full device block and identity — it opened fine and simply
	// has no SMART capability. Bit 2 means "some command failed", which real
	// devices set while returning complete, usable data. Keying on either bit
	// alone would discard good readings; keying on the device block matches
	// what the output actually contains.
	identified := out.Device.Name != ""
	problemBits := out.Smartctl.ExitStatus & (unreadableMask | exitCommandFailed)

	if problemBits != 0 && !identified {
		return nil, &DeviceFailure{
			Path:   path,
			Reason: classifyFailure(detail),
			Detail: detail,
		}
	}

	// Bit 0 is a usage error on our side and is never a usable reading,
	// whatever else the output contains.
	if out.Smartctl.ExitStatus&exitCommandLineError != 0 {
		return nil, &DeviceFailure{
			Path:   path,
			Reason: FailureOther,
			Detail: firstNonEmpty(detail, "smartctl reported a command line error"),
		}
	}

	// A device was identified but something failed. That is a partial read,
	// worth keeping and worth recording: it explains a missing field later,
	// and "the drive would not answer this query" can itself be a signal.
	if problemBits != 0 {
		messages = append(messages, Message{
			Severity: "warning",
			Text: fmt.Sprintf(
				"smartctl reported a partial read (exit status %d); some data may be missing",
				out.Smartctl.ExitStatus),
		})
	}

	dev := &Device{
		Path:            path,
		Transport:       classifyTransport(out.Device.Type, out.Device.Protocol),
		ModelName:       firstNonEmpty(out.ModelName, out.SCSIModelName, joinVendorProduct(out.SCSIVendor, out.SCSIProduct)),
		ModelFamily:     out.ModelFamily,
		SerialNumber:    out.SerialNumber,
		FirmwareVersion: out.FirmwareVersion,
		CapacityBytes:   parseCapacity(out.UserCapacity.BytesStr, out.UserCapacity.Bytes),
		RotationRate:    out.RotationRate,
		SMARTAvailable:  out.SmartSupport.Available,
		SMARTEnabled:    out.SmartSupport.Enabled,
		PowerCycleCount: out.PowerCycleCount,
		CollectedAt:     collectedAt(out.LocalTime.TimeT),
		SmartctlVersion: formatVersion(out.Smartctl.Version),
		Messages:        messages,
	}

	if out.SmartStatus != nil {
		passed := out.SmartStatus.Passed
		dev.HealthPassed = &passed
	}
	if out.Temperature != nil {
		dev.TemperatureCelsius = out.Temperature.Current
	}
	if out.PowerOnTime != nil {
		dev.PowerOnHours = out.PowerOnTime.Hours
	}

	switch dev.Transport {
	case TransportSATA:
		applyATA(dev, &out)
	case TransportNVMe:
		applyNVMe(dev, &out)
	case TransportSAS:
		applySCSI(dev, &out)
	default:
		// An unclassified transport may still have reported an attribute table
		// or a health log; take whatever is present rather than discarding it.
		applyATA(dev, &out)
		applyNVMe(dev, &out)
		applySCSI(dev, &out)
	}

	return dev, nil
}

// applyATA normalises a SATA attribute table.
func applyATA(dev *Device, out *smartctlOutput) {
	if out.ATASmartAttributes == nil {
		return
	}

	dev.Attributes = make([]Attribute, 0, len(out.ATASmartAttributes.Table))
	for _, a := range out.ATASmartAttributes.Table {
		attr := Attribute{
			ID:         a.ID,
			Name:       a.Name,
			Value:      a.Value,
			Worst:      a.Worst,
			Threshold:  a.Thresh,
			WhenFailed: a.WhenFailed,
			RawValue:   a.Raw.Value,
			RawString:  a.Raw.String,
			Prefailure: a.Flags.Prefailure,
		}
		dev.Attributes = append(dev.Attributes, attr)

		// Map the standardised attribute IDs onto the normalised counters.
		// Only IDs 1-199 are standardised; vendors define the rest freely, so
		// nothing above that range is mapped.
		raw := a.Raw.Value
		switch a.ID {
		case 5: // Reallocated_Sector_Ct
			dev.ReallocatedSectors = ptr(raw)
		case 9: // Power_On_Hours
			if dev.PowerOnHours == nil {
				dev.PowerOnHours = ptr(raw)
			}
		case 12: // Power_Cycle_Count
			if dev.PowerCycleCount == nil {
				dev.PowerCycleCount = ptr(raw)
			}
		case 187: // Reported_Uncorrect
			dev.UncorrectableErrors = ptr(raw)
		case 197: // Current_Pending_Sector
			dev.PendingSectors = ptr(raw)
		case 198: // Offline_Uncorrectable
			// Prefer 187 where both are present: it counts errors reported to
			// the host, which is the operationally relevant number.
			if dev.UncorrectableErrors == nil {
				dev.UncorrectableErrors = ptr(raw)
			}
		}
	}

	if out.ATAErrorLog != nil {
		dev.ErrorLogCount = ptr(out.ATAErrorLog.Summary.Count)
	}
	if out.ATASelfTestLog != nil {
		dev.SelfTestErrors = ptr(out.ATASelfTestLog.Standard.ErrorCountTotal)
	}
}

// applyNVMe normalises an NVMe health information log.
func applyNVMe(dev *Device, out *smartctlOutput) {
	log := out.NVMeHealthLog
	if log == nil {
		return
	}

	dev.CriticalWarning = log.CriticalWarning
	dev.AvailableSpare = log.AvailableSpare
	dev.AvailableSpareThreshold = log.AvailableSpareThreshold
	dev.PercentageUsed = log.PercentageUsed
	dev.MediaErrors = log.MediaErrors
	dev.ErrorLogCount = log.NumErrLogEntries

	// NVMe has no direct equivalent of reallocated or pending sectors. Media
	// errors are the closest analogue to an uncorrectable count, and are
	// mapped so transport-independent rules have something to read.
	if log.MediaErrors != nil {
		dev.UncorrectableErrors = log.MediaErrors
	}

	if dev.PowerOnHours == nil {
		dev.PowerOnHours = log.PowerOnHours
	}
	if dev.PowerCycleCount == nil {
		dev.PowerCycleCount = log.PowerCycles
	}
}

// applySCSI normalises SCSI and SAS error counters.
func applySCSI(dev *Device, out *smartctlOutput) {
	// The grown defect list is SAS's equivalent of reallocated sectors.
	if out.SCSIGrownDefectList != nil {
		dev.ReallocatedSectors = out.SCSIGrownDefectList
	}

	if log := out.SCSIErrorCounterLog; log != nil {
		var total uint64
		var seen bool
		if log.Read != nil && log.Read.TotalUncorrectedErrors != nil {
			total += *log.Read.TotalUncorrectedErrors
			seen = true
		}
		if log.Write != nil && log.Write.TotalUncorrectedErrors != nil {
			total += *log.Write.TotalUncorrectedErrors
			seen = true
		}
		if seen {
			dev.UncorrectableErrors = &total
		}
	}
}

// classifyFailure maps smartctl's error text onto a FailureReason.
//
// smartctl reports these only as prose, so matching on text is the only
// option. The patterns come from observed output of smartctl 7.4.
func classifyFailure(detail string) FailureReason {
	d := strings.ToLower(detail)

	switch {
	case strings.Contains(d, "permission denied"):
		return FailurePermission
	case strings.Contains(d, "no such file or directory"),
		strings.Contains(d, "unable to detect device type"):
		return FailureNotFound
	case strings.Contains(d, "device does not support smart"),
		strings.Contains(d, "smart support is: unavailable"),
		strings.Contains(d, "operation not supported"):
		return FailureUnsupported
	default:
		return FailureOther
	}
}

// classifyTransport maps smartctl's device type and protocol onto a Transport.
//
// The type field carries values like "sat", "ata", "nvme" and "scsi"; the
// protocol field is coarser but present when the type is ambiguous.
func classifyTransport(devType, protocol string) Transport {
	switch strings.ToLower(devType) {
	case "sat", "ata", "sata":
		return TransportSATA
	case "nvme":
		return TransportNVMe
	case "scsi", "sas", "sat+megaraid":
		return TransportSAS
	}

	switch strings.ToUpper(protocol) {
	case "ATA":
		return TransportSATA
	case "NVME":
		return TransportNVMe
	case "SCSI":
		return TransportSAS
	}
	return TransportUnknown
}

// parseCapacity prefers the exact string form over the numeric one.
//
// smartctl emits large capacities as both a JSON number and a string. The
// number loses precision past 2^53, which real NVMe output exceeds, so the
// string is authoritative where present.
func parseCapacity(str string, num json.Number) uint64 {
	if str != "" {
		if v, err := strconv.ParseUint(str, 10, 64); err == nil {
			return v
		}
	}
	if num != "" {
		if v, err := strconv.ParseUint(num.String(), 10, 64); err == nil {
			return v
		}
		// A value too large for uint64 is reported by some virtual devices;
		// an unusable capacity is not worth failing the whole reading over.
		if f, err := num.Float64(); err == nil && f >= 0 && f < 1<<63 {
			return uint64(f)
		}
	}
	return 0
}

// collectedAt converts smartctl's local_time to a UTC timestamp, falling back
// to now when absent.
func collectedAt(timeT int64) time.Time {
	if timeT == 0 {
		return time.Now().UTC()
	}
	return time.Unix(timeT, 0).UTC()
}

// formatVersion renders smartctl's [major, minor] version as "major.minor".
func formatVersion(v []int) string {
	if len(v) < 2 {
		return ""
	}
	return fmt.Sprintf("%d.%d", v[0], v[1])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinVendorProduct(vendor, product string) string {
	switch {
	case vendor != "" && product != "":
		return vendor + " " + product
	case product != "":
		return product
	default:
		return vendor
	}
}

func ptr[T any](v T) *T { return &v }
