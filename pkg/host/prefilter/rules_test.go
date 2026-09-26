package prefilter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/00Webbo/tropis/pkg/host/smart"
)

func u(v uint64) *uint64 { return &v }
func i(v int) *int       { return &v }
func b(v bool) *bool     { return &v }

// healthySATA is a synthetic device that fires no rule. Tests break one field
// at a time from here.
func healthySATA() *smart.Device {
	return &smart.Device{
		Path:                "/dev/sda",
		Transport:           smart.TransportSATA,
		ModelName:           "Test SSD",
		SerialNumber:        "SN1",
		HealthPassed:        b(true),
		TemperatureCelsius:  i(35),
		PowerOnHours:        u(10000),
		ReallocatedSectors:  u(0),
		PendingSectors:      u(0),
		UncorrectableErrors: u(0),
	}
}

func healthyNVMe() *smart.Device {
	return &smart.Device{
		Path:                    "/dev/nvme0n1",
		Transport:               smart.TransportNVMe,
		ModelName:               "Test NVMe",
		SerialNumber:            "SN2",
		HealthPassed:            b(true),
		TemperatureCelsius:      i(40),
		PowerOnHours:            u(10000),
		CriticalWarning:         u(0),
		PercentageUsed:          u(5),
		AvailableSpare:          u(100),
		AvailableSpareThreshold: u(10),
		MediaErrors:             u(0),
		UncorrectableErrors:     u(0),
	}
}

func ruleIDs(fs []Finding) []string {
	var ids []string
	for _, f := range fs {
		ids = append(ids, f.RuleID)
	}
	return ids
}

func TestRules(t *testing.T) {
	th := DefaultThresholds()

	tests := []struct {
		name   string
		cur    func() *smart.Device
		prev   func() *smart.Device
		want   []string
		reject []string
	}{
		{
			name: "healthy SATA fires nothing",
			cur:  healthySATA,
			want: nil,
		},
		{
			name: "healthy NVMe fires nothing",
			cur:  healthyNVMe,
			want: nil,
		},
		{
			name: "overall health failed",
			cur:  func() *smart.Device { d := healthySATA(); d.HealthPassed = b(false); return d },
			want: []string{RuleHealthFailed},
		},
		{
			name: "health not reported is not a failure",
			cur:  func() *smart.Device { d := healthySATA(); d.HealthPassed = nil; return d },
			want: nil,
		},
		{
			name: "prefail attribute failing now",
			cur: func() *smart.Device {
				d := healthySATA()
				d.Attributes = []smart.Attribute{{ID: 5, Name: "Reallocated_Sector_Ct", Prefailure: true, WhenFailed: "now"}}
				return d
			},
			want: []string{RuleAttributeFailing},
		},
		{
			name: "attribute that failed in the past does not fire",
			cur: func() *smart.Device {
				d := healthySATA()
				d.Attributes = []smart.Attribute{{ID: 5, Prefailure: true, WhenFailed: "past"}}
				return d
			},
			want: nil,
		},
		{
			name: "non-prefail attribute failing does not fire",
			cur: func() *smart.Device {
				d := healthySATA()
				d.Attributes = []smart.Attribute{{ID: 194, Prefailure: false, WhenFailed: "now"}}
				return d
			},
			want: nil,
		},
		{
			name: "pending sectors",
			cur:  func() *smart.Device { d := healthySATA(); d.PendingSectors = u(8); return d },
			want: []string{RulePendingSectors},
		},
		{
			name:   "small stable reallocated count does not fire",
			cur:    func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(12); return d },
			prev:   func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(12); return d },
			want:   nil,
			reject: []string{RuleReallocated, RuleReallocatedGrowth},
		},
		{
			name: "reallocated above absolute threshold",
			cur: func() *smart.Device {
				d := healthySATA()
				d.ReallocatedSectors = u(150)
				d.PowerOnHours = u(40000)
				return d
			},
			want: []string{RuleReallocated},
		},
		{
			name: "reallocated growth",
			cur:  func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(20); return d },
			prev: func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(12); return d },
			want: []string{RuleReallocatedGrowth},
		},
		{
			name:   "no previous reading means no growth",
			cur:    func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(20); return d },
			want:   nil,
			reject: []string{RuleReallocatedGrowth},
		},
		{
			name:   "counter decrease is not growth",
			cur:    func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(5); return d },
			prev:   func() *smart.Device { d := healthySATA(); d.ReallocatedSectors = u(20); return d },
			want:   nil,
			reject: []string{RuleReallocatedGrowth},
		},
		{
			name: "uncorrectable errors",
			cur:  func() *smart.Device { d := healthySATA(); d.UncorrectableErrors = u(3); return d },
			want: []string{RuleUncorrectable},
		},
		{
			name: "uncorrectable growth",
			cur:  func() *smart.Device { d := healthySATA(); d.UncorrectableErrors = u(9); return d },
			prev: func() *smart.Device { d := healthySATA(); d.UncorrectableErrors = u(3); return d },
			want: []string{RuleUncorrectableGrow, RuleUncorrectable},
		},
		{
			name: "NVMe media errors count as uncorrectable",
			cur:  func() *smart.Device { d := healthyNVMe(); d.MediaErrors = u(2); d.UncorrectableErrors = u(2); return d },
			want: []string{RuleUncorrectable},
		},
		{
			name: "temperature excursion",
			cur:  func() *smart.Device { d := healthySATA(); d.TemperatureCelsius = i(64); return d },
			want: []string{RuleTemperature},
		},
		{
			name: "temperature just under threshold",
			cur:  func() *smart.Device { d := healthySATA(); d.TemperatureCelsius = i(59); return d },
			want: nil,
		},
		{
			name: "NVMe wear",
			cur:  func() *smart.Device { d := healthyNVMe(); d.PercentageUsed = u(95); return d },
			want: []string{RuleNVMeWear},
		},
		{
			name: "NVMe spare below threshold",
			cur:  func() *smart.Device { d := healthyNVMe(); d.AvailableSpare = u(5); return d },
			want: []string{RuleNVMeWear},
		},
		{
			name: "NVMe spare threshold of zero means not reported",
			cur: func() *smart.Device {
				d := healthyNVMe()
				d.AvailableSpare = u(0)
				d.AvailableSpareThreshold = u(0)
				return d
			},
			want: nil,
		},
		{
			name: "NVMe critical warning",
			cur:  func() *smart.Device { d := healthyNVMe(); d.CriticalWarning = u(0x04); return d },
			want: []string{RuleNVMeCritical},
		},
		{
			// 60 defects over 2000h is 30/1000h: a young drive failing fast,
			// which the absolute reallocated threshold misses by design.
			name: "defect density on a young drive",
			cur: func() *smart.Device {
				d := healthySATA()
				d.PowerOnHours = u(2000)
				d.ReallocatedSectors = u(60)
				return d
			},
			want: []string{RuleDefectDensity},
		},
		{
			name: "same defect count on an old drive is below density threshold",
			cur: func() *smart.Device {
				d := healthySATA()
				d.PowerOnHours = u(50000)
				d.ReallocatedSectors = u(60)
				return d
			},
			want: nil,
		},
		{
			name: "defect density not computed on a very new drive",
			cur: func() *smart.Device {
				d := healthySATA()
				d.PowerOnHours = u(100)
				d.ReallocatedSectors = u(8)
				return d
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prev *smart.Device
			if tt.prev != nil {
				prev = tt.prev()
			}
			got := ruleIDs(EvaluateDevice(tt.cur(), prev, th))

			if tt.want == nil {
				if len(got) != 0 && tt.reject == nil {
					t.Errorf("expected no findings, got %v", got)
				}
			} else if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findings = %v, want %v", got, tt.want)
			}
			for _, r := range tt.reject {
				for _, g := range got {
					if g == r {
						t.Errorf("rule %s should not fire, got %v", r, got)
					}
				}
			}
		})
	}
}

func TestEvaluateDeviceNil(t *testing.T) {
	if got := EvaluateDevice(nil, nil, DefaultThresholds()); got != nil {
		t.Errorf("nil device should produce no findings, got %v", got)
	}
}

// Every rule has a unique ID in the smart. namespace; IDs are part of the
// output contract.
func TestRuleIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range RuleIDs() {
		if seen[id] {
			t.Errorf("duplicate rule ID %q", id)
		}
		seen[id] = true
		if !strings.HasPrefix(id, "smart.") {
			t.Errorf("rule ID %q should be in the smart. namespace", id)
		}
	}
}

// The committed smartctl samples, parsed by the real parser and run through
// the real rules.
func TestRulesAgainstSamples(t *testing.T) {
	load := func(name, path string) *smart.Device {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "smart", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		d, fail := smart.Parse(data, path)
		if fail != nil {
			t.Fatalf("parse %s: %v", name, fail)
		}
		return d
	}
	th := DefaultThresholds()

	for _, s := range []struct{ file, path string }{
		{"sata-healthy.json", "/dev/sda"},
		{"nvme-healthy.json", "/dev/nvme0n1"},
	} {
		if got := EvaluateDevice(load(s.file, s.path), nil, th); len(got) != 0 {
			t.Errorf("%s: healthy sample fired %v", s.file, got)
		}
	}

	got := ruleIDs(EvaluateDevice(load("sata-failing.json", "/dev/sdb"), nil, th))
	for _, want := range []string{RuleHealthFailed, RulePendingSectors, RuleReallocated, RuleUncorrectable, RuleDefectDensity} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("failing sample should fire %s, got %v", want, got)
		}
	}
	if got[0] != RuleHealthFailed {
		t.Errorf("health failure should be the first finding, got %v", got)
	}
}
