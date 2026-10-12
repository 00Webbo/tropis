package capture

import (
	"fmt"
	"sort"
	"strings"

	"github.com/00Webbo/tropis/eval/scenarios"
	"github.com/00Webbo/tropis/pkg/host/smart"
	"github.com/00Webbo/tropis/pkg/schema"
)

// counters reads each expectSmart counter from a parsed reading.
var counters = map[string]func(*smart.Device) *uint64{
	scenarios.CounterPending:       func(d *smart.Device) *uint64 { return d.PendingSectors },
	scenarios.CounterReallocated:   func(d *smart.Device) *uint64 { return d.ReallocatedSectors },
	scenarios.CounterUncorrectable: func(d *smart.Device) *uint64 { return d.UncorrectableErrors },
}

// CheckSMARTChange verifies that a scenario expecting a real SMART effect
// shows one in the fixture: on the drive with the given serial, at least one
// of the expected counters must be higher in the capture's reading than in
// the collector's earlier one (HostCapture.Previous).
//
// The drive is found by serial, never by path: kernel names can move between
// the two readings, and a counter that grew on some other drive says nothing
// about the one the fault was injected on. The fixture's own readings are
// what is checked, rather than the injection script's, because they are what
// the evaluation replays: a label claiming a SMART-visible fault must be
// true of the evidence the model is given.
func CheckSMARTChange(scenarioID string, expect []string, serial string, host *schema.HostCapture) error {
	if len(expect) == 0 {
		return nil
	}
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return refuse("scenario %s expects %s to grow, but the injection record does not name the drive's serial; "+
			"re-run the injection with a current hack/inject (its smartBefore carries serial=)", scenarioID, orList(expect))
	}
	drive := "drive " + serial
	if host == nil {
		return refuse("scenario %s: no host capture to check %s against", scenarioID, drive)
	}
	afterPath, after, err := findSerial(host.SMART, serial)
	if err != nil {
		return refuse("scenario %s: the capture's SMART reading %v", scenarioID, err)
	}
	if after == nil {
		return refuse("scenario %s expects %s to grow on %s, but the capture has no SMART reading of that drive",
			scenarioID, orList(expect), drive)
	}
	drive += " (" + afterPath + ")"
	if host.Previous == nil {
		return refuse("scenario %s expects %s to grow on %s, but the capture has no earlier reading to compare with; "+
			"restart the collector before injecting so its oldest reading is pre-fault", scenarioID, orList(expect), drive)
	}
	beforePath, before, err := findSerial(host.Previous.SMART, serial)
	if err != nil {
		return refuse("scenario %s: the earlier SMART reading %v", scenarioID, err)
	}
	if before == nil {
		return refuse("scenario %s expects %s to grow on %s, but the earlier reading (%s) does not include that drive",
			scenarioID, orList(expect), drive, host.Previous.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if beforePath != afterPath {
		drive = fmt.Sprintf("drive %s (%s, earlier %s)", serial, afterPath, beforePath)
	}

	grew := false
	var seen []string
	for _, c := range expect {
		get, ok := counters[c]
		if !ok {
			return fmt.Errorf("scenario %s: unknown expectSmart counter %q", scenarioID, c)
		}
		b, a := get(before), get(after)
		seen = append(seen, fmt.Sprintf("%s %s -> %s", c, show(b), show(a)))
		if b != nil && a != nil && *a > *b {
			grew = true
		}
	}
	if !grew {
		return refuse("scenario %s expects %s to grow on %s, but SMART did not show it between %s and %s: %s",
			scenarioID, orList(expect), drive,
			host.Previous.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"), host.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"),
			strings.Join(seen, ", "))
	}
	return nil
}

// findSerial returns the reading of the drive with the given serial, or nil
// when no readable device has it. Two devices claiming one serial is an
// error: there would be no telling which reading is the drive's.
func findSerial(readings map[string]schema.RawJSON, serial string) (string, *smart.Device, error) {
	paths := make([]string, 0, len(readings))
	for p := range readings {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var foundPath string
	var found *smart.Device
	for _, p := range paths {
		d, fail := smart.Parse(readings[p], p)
		if fail != nil || strings.TrimSpace(d.SerialNumber) != serial {
			continue
		}
		if found != nil {
			return "", nil, fmt.Errorf("has two devices with serial %s (%s and %s)", serial, foundPath, p)
		}
		foundPath, found = p, d
	}
	return foundPath, found, nil
}

// SerialFromRecord returns the target drive's serial as the injection
// script recorded it in its SMART snapshots ("... serial=<sn>"), or "".
func SerialFromRecord(rec *InjectionRecord) string {
	if rec == nil || rec.Injection == nil {
		return ""
	}
	for _, key := range []string{"smartBefore", "smartAfterInjection", "smartAfter"} {
		snap := rec.Injection.Params[key]
		// serial is the snapshot's last field, so it may hold spaces.
		if i := strings.Index(snap, "serial="); i >= 0 && (i == 0 || snap[i-1] == ' ') {
			if s := strings.TrimSpace(snap[i+len("serial="):]); s != "" {
				return s
			}
		}
	}
	return ""
}

func show(v *uint64) string {
	if v == nil {
		return "not reported"
	}
	return fmt.Sprint(*v)
}

func orList(c []string) string {
	return strings.Join(c, " or ")
}

// CheckStableDefects verifies that a scenario declaring hardware
// scarred-sata was captured on a drive with old, stable defects: reallocated
// sectors above zero in both the capture's reading and the collector's
// earlier one, unchanged between them, and no pending sectors in either. A
// clean drive, or one whose defects are still developing, would make the
// label's "the defects are not moving" untrue of the evidence; pending
// sectors mean the drive is actively failing, and the disk could then be
// the cause.
//
// These scenarios inject nothing on the drive, so there is no injection
// record to name it. The drive is the inventory disk named with --disk,
// found in both readings by that entry's serial: its kernel path may have
// moved, and a reading of some other drive says nothing about this one.
func CheckStableDefects(scenarioID, serial string, host *schema.HostCapture) error {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return refuse("scenario %s needs the scarred drive; name it with --disk, and give that inventory disk its serial", scenarioID)
	}
	drive := "drive " + serial
	if host == nil {
		return refuse("scenario %s: no host capture to check %s against", scenarioID, drive)
	}
	path, d, err := findSerial(host.SMART, serial)
	if err != nil {
		return refuse("scenario %s: the capture's SMART reading %v", scenarioID, err)
	}
	if d == nil {
		return refuse("scenario %s needs stable defects on %s, but the capture has no SMART reading of it", scenarioID, drive)
	}
	drive += " (" + path + ")"
	if host.Previous == nil {
		return refuse("scenario %s needs stable defects on %s, but the capture has no earlier reading to compare with; "+
			"leave the collector running for 24 h before capturing", scenarioID, drive)
	}
	_, before, err := findSerial(host.Previous.SMART, serial)
	if err != nil {
		return refuse("scenario %s: the earlier SMART reading %v", scenarioID, err)
	}
	if before == nil {
		return refuse("scenario %s needs stable defects on %s, but the earlier reading (%s) does not include that drive",
			scenarioID, drive, host.Previous.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}

	rb, ra := before.ReallocatedSectors, d.ReallocatedSectors
	pb, pa := before.PendingSectors, d.PendingSectors
	stable := rb != nil && ra != nil && *rb > 0 && *ra == *rb &&
		pb != nil && pa != nil && *pb == 0 && *pa == 0
	if !stable {
		return refuse("scenario %s needs stable defects on %s (reallocated above zero and unchanged, pending 0), "+
			"but between %s and %s SMART showed %s %s -> %s, %s %s -> %s",
			scenarioID, drive,
			host.Previous.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"), host.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"),
			scenarios.CounterReallocated, show(rb), show(ra), scenarios.CounterPending, show(pb), show(pa))
	}
	return nil
}
