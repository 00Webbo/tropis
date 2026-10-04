package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/00Webbo/tropis/pkg/host/prefilter"
	"github.com/00Webbo/tropis/pkg/schema"
)

// sampleDir builds a --from-dir directory from committed smartctl samples,
// renaming each to the device it stands for.
func sampleDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for dev, sample := range files {
		data, err := os.ReadFile(filepath.Join("..", "..", "pkg", "host", "smart", "testdata", sample))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dev+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runCmd(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// The T4 acceptance criterion at the binary boundary: exit 0/1/2 per NPD
// protocol, one line of at most 80 characters on stdout.
func TestPrefilterNPDProtocol(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		wantCode int
		wantMsg  string
	}{
		{
			name:     "healthy disks exit 0",
			files:    map[string]string{"sda": "sata-healthy.json", "nvme0n1": "nvme-healthy.json"},
			wantCode: prefilter.NPDOK,
			wantMsg:  "SMART OK",
		},
		{
			name:     "failing disk exits 1",
			files:    map[string]string{"sda": "sata-healthy.json", "sdb": "sata-failing.json"},
			wantCode: prefilter.NPDProblem,
			wantMsg:  "sdb: overall health FAILED",
		},
		{
			name:     "unreadable disk exits 2",
			files:    map[string]string{"sda": "error-permission-denied.json"},
			wantCode: prefilter.NPDUnknown,
			wantMsg:  "SMART unreadable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, _ := runCmd("prefilter", "--npd", "--from-dir", sampleDir(t, tt.files))
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stdout %q)", code, tt.wantCode, out)
			}
			line := strings.TrimRight(out, "\n")
			if strings.Contains(line, "\n") {
				t.Errorf("NPD output must be a single line, got %q", out)
			}
			if len(line) > prefilter.NPDMaxOutput {
				t.Errorf("output is %d bytes, over the NPD budget: %q", len(line), line)
			}
			if !strings.HasPrefix(line, tt.wantMsg) {
				t.Errorf("output = %q, want prefix %q", line, tt.wantMsg)
			}
		})
	}
}

// Any failure in NPD mode is Unknown with a one-line message, never a usage
// dump or a Go error that NPD would turn into a nonsense condition.
func TestPrefilterNPDFailureIsUnknown(t *testing.T) {
	code, out, _ := runCmd("prefilter", "--npd", "--from-dir", t.TempDir())
	if code != prefilter.NPDUnknown {
		t.Errorf("exit code = %d, want %d", code, prefilter.NPDUnknown)
	}
	if out == "" || strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Errorf("want one line on stdout, got %q", out)
	}

	code, _, _ = runCmd("prefilter", "--npd", "--no-such-flag")
	if code != prefilter.NPDUnknown {
		t.Errorf("bad flag in NPD mode: exit code = %d, want %d", code, prefilter.NPDUnknown)
	}
}

// Growth rules work across separate process invocations via the state dir,
// which is how NPD runs a plugin.
func TestPrefilterGrowthAcrossRuns(t *testing.T) {
	state := t.TempDir()

	// First run records a healthy baseline.
	first := sampleDir(t, map[string]string{"sda": "sata-healthy.json"})
	if code, out, _ := runCmd("prefilter", "--npd", "--state-dir", state, "--from-dir", first); code != prefilter.NPDOK {
		t.Fatalf("baseline run: exit %d, %q", code, out)
	}

	// Second run: same drive (same serial), reallocated count has grown.
	data, err := os.ReadFile(filepath.Join(first, "sda.json"))
	if err != nil {
		t.Fatal(err)
	}
	grown := bytes.Replace(data,
		[]byte(`"raw": {
          "value": 0,
          "string": "0"
        }
      },
      {
        "id": 9,`),
		[]byte(`"raw": {
          "value": 7,
          "string": "7"
        }
      },
      {
        "id": 9,`), 1)
	if bytes.Equal(grown, data) {
		t.Fatal("test setup: failed to modify reallocated count in sample")
	}
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "sda.json"), grown, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, _ := runCmd("prefilter", "--npd", "--state-dir", state, "--from-dir", second)
	if code != prefilter.NPDProblem {
		t.Fatalf("growth run: exit %d, want %d (%q)", code, prefilter.NPDProblem, out)
	}
	if !strings.Contains(out, "reallocated +7") {
		t.Errorf("output should report the growth, got %q", out)
	}
}

func TestPrefilterJSON(t *testing.T) {
	code, out, errOut := runCmd("prefilter", "--from-dir", sampleDir(t, map[string]string{"sdb": "sata-failing.json"}))
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var res prefilter.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output is not a JSON Result: %v\n%s", err, out)
	}
	if !res.Candidate() {
		t.Error("failing disk should make the node a candidate")
	}
}

func TestPrefilterThresholdOverride(t *testing.T) {
	dir := sampleDir(t, map[string]string{"sda": "sata-healthy.json"})
	th := filepath.Join(t.TempDir(), "th.json")
	// The healthy sample runs at 32C. Lower the limit so it fires; the file
	// names only the threshold it changes.
	if err := os.WriteFile(th, []byte(`{"temperatureMaxC": 30}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCmd("prefilter", "--npd", "--thresholds", th, "--from-dir", dir)
	if code != prefilter.NPDProblem || !strings.Contains(out, "temperature 32C") {
		t.Errorf("exit %d, %q", code, out)
	}
}

func TestCollectFromDir(t *testing.T) {
	code, out, errOut := runCmd("collect", "--from-dir",
		sampleDir(t, map[string]string{"sda": "sata-healthy.json", "sdx": "error-missing-device.json"}))
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, `"devices"`) || !strings.Contains(out, `"failures"`) {
		t.Errorf("report should carry both devices and failures:\n%s", out)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := runCmd(); code != exitUsage {
		t.Errorf("no args: exit %d", code)
	}
	if code, _, _ := runCmd("frobnicate"); code != exitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
}

func TestServeHandler(t *testing.T) {
	calls := 0
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	cache := &captureCache{
		collect: func(context.Context) (map[string][]byte, error) {
			calls++
			return map[string][]byte{"/dev/sda": []byte(`{"device":{"name":"/dev/sda"}}`)}, nil
		},
		minInterval: time.Minute,
		now:         func() time.Time { return now },
	}
	srv := httptest.NewServer(cache.handler())
	defer srv.Close()

	get := func() schema.HostCapture {
		t.Helper()
		resp, err := http.Get(srv.URL + SMARTPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		var hc schema.HostCapture
		if err := json.NewDecoder(resp.Body).Decode(&hc); err != nil {
			t.Fatal(err)
		}
		return hc
	}

	hc := get()
	if !strings.Contains(hc.SMART["/dev/sda"].String(), `"/dev/sda"`) {
		t.Errorf("raw capture not served verbatim: %v", hc.SMART)
	}
	get()
	if calls != 1 {
		t.Errorf("second request within min-interval collected again: %d calls", calls)
	}
	now = now.Add(2 * time.Minute)
	get()
	if calls != 2 {
		t.Errorf("request after min-interval should re-collect: %d calls", calls)
	}
}

func TestServeCollectionError(t *testing.T) {
	cache := &captureCache{
		collect:     func(context.Context) (map[string][]byte, error) { return nil, errors.New("smartctl not found") },
		minInterval: time.Minute,
		now:         time.Now,
	}
	rec := httptest.NewRecorder()
	cache.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SMARTPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// The capture carries the oldest snapshot in its history as Previous, so the
// reasoning layer can tell a stable defect count from a growing one.
func TestServeHistory(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	reading := 0
	cache := &captureCache{
		collect: func(context.Context) (map[string][]byte, error) {
			reading++
			return map[string][]byte{"/dev/sda": []byte(fmt.Sprintf(`{"reading":%d}`, reading))}, nil
		},
		minInterval:     time.Minute,
		historyInterval: time.Hour,
		historyMax:      24 * time.Hour,
		now:             func() time.Time { return now },
	}
	ctx := context.Background()

	first, err := cache.get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Previous != nil {
		t.Error("the first capture has no history to compare against")
	}

	// Hourly for 30 hours: history is capped at 24h back.
	var last *schema.HostCapture
	for h := 1; h <= 30; h++ {
		now = now.Add(time.Hour)
		if last, err = cache.get(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if last.Previous == nil {
		t.Fatal("capture should carry a previous snapshot")
	}
	if age := last.CollectedAt.Sub(last.Previous.CollectedAt); age != 24*time.Hour {
		t.Errorf("previous snapshot is %v old, want 24h", age)
	}
	if last.Previous.SMART["/dev/sda"].String() == last.SMART["/dev/sda"].String() {
		t.Error("previous should be an earlier reading")
	}

	// Requests between history intervals do not add snapshots.
	n := len(cache.history)
	now = now.Add(2 * time.Minute)
	if _, err := cache.get(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cache.history) > n {
		t.Errorf("history grew from %d to %d within one interval", n, len(cache.history))
	}
}
