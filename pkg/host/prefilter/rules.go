// Package prefilter holds the deterministic rules that decide which nodes are
// worth a reasoning pass.
//
// The pre-filter exists for two reasons of equal weight. It keeps the model
// call rare, which keeps it cheap. And it keeps the model's input small and
// relevant: a model handed every node's SMART dump and asked "is this causing
// that?" is heavily biased toward yes.
//
// Rules are pure functions of a current reading and, where a rule measures
// growth, the previous reading of the same device. They are written so their
// output maps directly onto node-problem-detector's custom plugin protocol —
// see npd.go — which lets one artifact serve as both Tropis's pre-filter and
// an NPD plugin.
//
// A rule firing means "look at this node", never "this node is broken". The
// reasoning layer decides what, if anything, the signal means.
package prefilter

import (
	"fmt"

	"github.com/00Webbo/tropis/pkg/host/smart"
)

// Rule IDs. These appear in Verdict.TriggeredBy and in NPD condition reasons,
// so they are part of the output contract.
const (
	RuleHealthFailed      = "smart.health_failed"
	RuleAttributeFailing  = "smart.attribute_failing"
	RuleReallocated       = "smart.reallocated_sectors"
	RuleReallocatedGrowth = "smart.reallocated_growth"
	RulePendingSectors    = "smart.pending_sectors"
	RuleUncorrectable     = "smart.uncorrectable_errors"
	RuleUncorrectableGrow = "smart.uncorrectable_growth"
	RuleTemperature       = "smart.temperature"
	RuleNVMeWear          = "smart.nvme_wear"
	RuleNVMeCritical      = "smart.nvme_critical_warning"
	RuleDefectDensity     = "smart.defect_density"
)

// Thresholds configures the rules. The zero value is not useful; start from
// DefaultThresholds.
type Thresholds struct {
	// ReallocatedMax is the absolute reallocated sector count at or above
	// which a device is raised. Deliberately high: plenty of healthy old
	// drives carry a small, stable count, and growth is the better signal.
	ReallocatedMax uint64 `json:"reallocatedMax"`

	// ReallocatedGrowthMin is the increase since the previous reading that
	// raises a device. Any growth at all is interesting.
	ReallocatedGrowthMin uint64 `json:"reallocatedGrowthMin"`

	// PendingMin is the pending sector count at or above which a device is
	// raised. Pending sectors are usually the earliest actionable signal.
	PendingMin uint64 `json:"pendingMin"`

	// UncorrectableMin is the absolute uncorrectable error count that raises
	// a device.
	UncorrectableMin uint64 `json:"uncorrectableMin"`

	// UncorrectableGrowthMin is the increase since the previous reading that
	// raises a device.
	UncorrectableGrowthMin uint64 `json:"uncorrectableGrowthMin"`

	// TemperatureMaxC is the temperature at or above which a device is raised.
	TemperatureMaxC int `json:"temperatureMaxC"`

	// NVMePercentageUsedMax is the NVMe wear indicator at or above which a
	// device is raised. 100 means rated endurance is consumed.
	NVMePercentageUsedMax uint64 `json:"nvmePercentageUsedMax"`

	// DefectsPer1000HoursMax is reallocated plus pending sectors per thousand
	// power-on hours. It flags a young drive accumulating defects quickly,
	// which the absolute threshold misses by design.
	DefectsPer1000HoursMax float64 `json:"defectsPer1000HoursMax"`

	// DefectDensityMinHours is the power-on age below which defect density is
	// not computed, since a handful of factory remaps on a new drive would
	// otherwise dominate the ratio.
	DefectDensityMinHours uint64 `json:"defectDensityMinHours"`
}

// DefaultThresholds returns conservative defaults.
//
// These are starting points, not tuned values. Tuning happens against the
// eval corpus, and a threshold change is a result-affecting change like a
// prompt change.
func DefaultThresholds() Thresholds {
	return Thresholds{
		ReallocatedMax:         100,
		ReallocatedGrowthMin:   1,
		PendingMin:             1,
		UncorrectableMin:       1,
		UncorrectableGrowthMin: 1,
		TemperatureMaxC:        60,
		NVMePercentageUsedMax:  90,
		DefectsPer1000HoursMax: 10,
		DefectDensityMinHours:  500,
	}
}

// Finding is one rule firing on one device.
type Finding struct {
	// RuleID identifies the rule.
	RuleID string `json:"ruleId"`

	// Device is the device path the rule fired on.
	Device string `json:"device"`

	// Summary is a short human-readable description, suitable for the 80
	// character NPD message budget when combined with the device name.
	Summary string `json:"summary"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s %s: %s", f.Device, f.RuleID, f.Summary)
}

// rule evaluates one condition. prev may be nil.
type rule struct {
	id   string
	eval func(cur, prev *smart.Device, th Thresholds) (summary string, fired bool)
}

// rules is the ordered rule set. Order determines NPD message priority: the
// most decisive signal is listed first so it survives truncation.
var rules = []rule{
	{RuleHealthFailed, func(cur, _ *smart.Device, _ Thresholds) (string, bool) {
		if cur.HealthPassed != nil && !*cur.HealthPassed {
			return "overall health FAILED", true
		}
		return "", false
	}},

	{RuleNVMeCritical, func(cur, _ *smart.Device, _ Thresholds) (string, bool) {
		if cur.CriticalWarning != nil && *cur.CriticalWarning != 0 {
			return fmt.Sprintf("NVMe critical warning 0x%02x", *cur.CriticalWarning), true
		}
		return "", false
	}},

	{RuleAttributeFailing, func(cur, _ *smart.Device, _ Thresholds) (string, bool) {
		for _, a := range cur.Attributes {
			if a.Prefailure && a.Failed() {
				return fmt.Sprintf("attr %d %s below threshold", a.ID, a.Name), true
			}
		}
		return "", false
	}},

	{RulePendingSectors, func(cur, _ *smart.Device, th Thresholds) (string, bool) {
		if v := cur.PendingSectors; v != nil && *v >= th.PendingMin {
			return fmt.Sprintf("pending sectors %d", *v), true
		}
		return "", false
	}},

	{RuleReallocatedGrowth, func(cur, prev *smart.Device, th Thresholds) (string, bool) {
		if d, ok := growth(cur.ReallocatedSectors, prevField(prev, func(p *smart.Device) *uint64 { return p.ReallocatedSectors })); ok && d >= th.ReallocatedGrowthMin {
			return fmt.Sprintf("reallocated +%d", d), true
		}
		return "", false
	}},

	{RuleUncorrectableGrow, func(cur, prev *smart.Device, th Thresholds) (string, bool) {
		if d, ok := growth(cur.UncorrectableErrors, prevField(prev, func(p *smart.Device) *uint64 { return p.UncorrectableErrors })); ok && d >= th.UncorrectableGrowthMin {
			return fmt.Sprintf("uncorrectable +%d", d), true
		}
		return "", false
	}},

	{RuleReallocated, func(cur, _ *smart.Device, th Thresholds) (string, bool) {
		if v := cur.ReallocatedSectors; v != nil && *v >= th.ReallocatedMax {
			return fmt.Sprintf("reallocated %d", *v), true
		}
		return "", false
	}},

	{RuleUncorrectable, func(cur, _ *smart.Device, th Thresholds) (string, bool) {
		if v := cur.UncorrectableErrors; v != nil && *v >= th.UncorrectableMin {
			return fmt.Sprintf("uncorrectable %d", *v), true
		}
		return "", false
	}},

	{RuleDefectDensity, func(cur, _ *smart.Device, th Thresholds) (string, bool) {
		if cur.PowerOnHours == nil || *cur.PowerOnHours < th.DefectDensityMinHours {
			return "", false
		}
		var defects uint64
		var seen bool
		for _, v := range []*uint64{cur.ReallocatedSectors, cur.PendingSectors} {
			if v != nil {
				defects += *v
				seen = true
			}
		}
		if !seen {
			return "", false
		}
		density := float64(defects) / (float64(*cur.PowerOnHours) / 1000)
		if density >= th.DefectsPer1000HoursMax {
			return fmt.Sprintf("%.0f defects/1000h", density), true
		}
		return "", false
	}},

	{RuleNVMeWear, func(cur, _ *smart.Device, th Thresholds) (string, bool) {
		if v := cur.PercentageUsed; v != nil && *v >= th.NVMePercentageUsedMax {
			return fmt.Sprintf("wear %d%% used", *v), true
		}
		// Spare below threshold is a defined NVMe failure condition. A
		// threshold of zero means the controller does not report one.
		if s, t := cur.AvailableSpare, cur.AvailableSpareThreshold; s != nil && t != nil && *t > 0 && *s < *t {
			return fmt.Sprintf("spare %d%% < %d%%", *s, *t), true
		}
		return "", false
	}},

	{RuleTemperature, func(cur, _ *smart.Device, th Thresholds) (string, bool) {
		if v := cur.TemperatureCelsius; v != nil && *v >= th.TemperatureMaxC {
			return fmt.Sprintf("temperature %dC", *v), true
		}
		return "", false
	}},
}

// RuleIDs returns every rule ID in priority order.
func RuleIDs() []string {
	ids := make([]string, len(rules))
	for i, r := range rules {
		ids[i] = r.id
	}
	return ids
}

// EvaluateDevice runs every rule against one device. prev is the previous
// reading of the same physical device, or nil when there is none; growth
// rules are silent without one.
func EvaluateDevice(cur, prev *smart.Device, th Thresholds) []Finding {
	if cur == nil {
		return nil
	}
	var out []Finding
	for _, r := range rules {
		if summary, fired := r.eval(cur, prev, th); fired {
			out = append(out, Finding{RuleID: r.id, Device: cur.Path, Summary: summary})
		}
	}
	return out
}

// growth returns cur-prev when both are present and cur exceeds prev.
//
// A decrease is not growth. It usually means the drive was replaced under the
// same path, which the state store guards against by keying on serial number,
// or a vendor counter reset.
func growth(cur, prev *uint64) (uint64, bool) {
	if cur == nil || prev == nil || *cur <= *prev {
		return 0, false
	}
	return *cur - *prev, true
}

func prevField(prev *smart.Device, get func(*smart.Device) *uint64) *uint64 {
	if prev == nil {
		return nil
	}
	return get(prev)
}
