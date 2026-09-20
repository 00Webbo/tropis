package smart

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeRunner returns canned output per device argument.
func fakeRunner(t *testing.T, responses map[string][]byte, errs map[string]error) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		// The device path is the final argument for both -a and --scan forms.
		key := args[len(args)-1]
		if err, ok := errs[key]; ok {
			return nil, err
		}
		if out, ok := responses[key]; ok {
			return out, nil
		}
		return []byte("{}"), nil
	}
}

func TestCollectMixedResults(t *testing.T) {
	healthy := readSample(t, "sata-healthy.json")
	failing := readSample(t, "sata-failing.json")
	denied := readSample(t, "error-permission-denied.json")

	c := &Collector{
		runner: fakeRunner(t, map[string][]byte{
			"/dev/sda": healthy,
			"/dev/sdb": failing,
			"/dev/sdc": denied,
		}, nil),
	}

	report, err := c.Collect(context.Background(), []string{"/dev/sda", "/dev/sdb", "/dev/sdc"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(report.Devices) != 2 {
		t.Errorf("read %d devices, want 2", len(report.Devices))
	}
	if len(report.Failures) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(report.Failures))
	}
	if report.Failures[0].Reason != FailurePermission {
		t.Errorf("failure reason = %q, want %q", report.Failures[0].Reason, FailurePermission)
	}
	if report.CollectedAt.IsZero() {
		t.Error("collectedAt should be set")
	}
}

// One unreadable disk must not hide the state of the others.
func TestCollectContinuesAfterFailure(t *testing.T) {
	c := &Collector{
		runner: fakeRunner(t,
			map[string][]byte{"/dev/sdb": readSample(t, "sata-healthy.json")},
			map[string]error{"/dev/sda": errors.New("device exploded")},
		),
	}

	report, err := c.Collect(context.Background(), []string{"/dev/sda", "/dev/sdb"})
	if err != nil {
		t.Fatalf("a single device failure must not abort the sweep: %v", err)
	}
	if len(report.Devices) != 1 {
		t.Errorf("read %d devices, want the one that worked", len(report.Devices))
	}
	if len(report.Failures) != 1 {
		t.Errorf("recorded %d failures, want 1", len(report.Failures))
	}
}

// "No disks are failing" and "we were never able to look" are very different
// statements, so a missing smartctl is an error, not an empty report.
func TestCollectRequiresSmartctl(t *testing.T) {
	c := &Collector{
		runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, exec.ErrNotFound
		},
	}

	_, err := c.Collect(context.Background(), []string{"/dev/sda"})
	if err == nil {
		t.Fatal("expected an error when smartctl is absent")
	}
	if !strings.Contains(err.Error(), "smartmontools") {
		t.Errorf("error should tell the operator what to install, got: %v", err)
	}
}

func TestAvailable(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		c := &Collector{runner: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("smartctl 7.4"), nil
		}}
		if err := c.Available(context.Background()); err != nil {
			t.Errorf("Available: %v", err)
		}
	})

	t.Run("absent", func(t *testing.T) {
		c := &Collector{runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, exec.ErrNotFound
		}}
		if err := c.Available(context.Background()); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestScan(t *testing.T) {
	out := `{"devices":[
		{"name":"/dev/sda","type":"sat"},
		{"name":"/dev/nvme0","type":"nvme"}
	]}`

	c := &Collector{runner: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), nil
	}}

	devices, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(devices) != 2 || devices[0] != "/dev/sda" || devices[1] != "/dev/nvme0" {
		t.Errorf("Scan = %v", devices)
	}
}

// A machine with no disks visible to smartctl scans to an empty list, not an
// error.
func TestScanEmpty(t *testing.T) {
	c := &Collector{runner: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"devices":[]}`), nil
	}}

	devices, err := c.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("Scan = %v, want empty", devices)
	}
}

func TestCollectDeviceEmptyOutput(t *testing.T) {
	c := &Collector{runner: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("   "), nil
	}}

	dev, fail := c.CollectDevice(context.Background(), "/dev/sda")
	if fail == nil {
		t.Fatalf("expected a failure, got %+v", dev)
	}
	if fail.Reason != FailureParse {
		t.Errorf("reason = %q, want %q", fail.Reason, FailureParse)
	}
}

// A drive that hangs on a SMART query is itself a signal and must not stall
// the sweep.
func TestCollectDeviceTimeout(t *testing.T) {
	c := &Collector{runner: func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}

	_, fail := c.CollectDevice(context.Background(), "/dev/sda")
	if fail == nil {
		t.Fatal("expected a failure")
	}
	if fail.Reason != FailureTimeout {
		t.Errorf("reason = %q, want %q", fail.Reason, FailureTimeout)
	}
}

func TestCollectorDefaults(t *testing.T) {
	c := &Collector{}
	if c.binary() != DefaultBinary {
		t.Errorf("binary = %q, want %q", c.binary(), DefaultBinary)
	}
	if c.timeout() != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", c.timeout(), DefaultTimeout)
	}

	custom := &Collector{Binary: "/usr/local/sbin/smartctl", Timeout: time.Second}
	if custom.binary() != "/usr/local/sbin/smartctl" {
		t.Errorf("binary = %q", custom.binary())
	}
	if custom.timeout() != time.Second {
		t.Errorf("timeout = %v", custom.timeout())
	}
}
