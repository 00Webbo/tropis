package prefilter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nathanwebb/tropis/pkg/host/smart"
)

// StateFile is the file, within a state directory, holding previous readings.
const StateFile = "smart-state.json"

// LoadState reads previous readings from dir, keyed by DeviceKey.
//
// A missing state file is not an error: it is the first run, and growth rules
// simply stay silent until there is something to compare against. A corrupt
// state file is also tolerated, with an error returned for logging, because
// losing one growth comparison is far better than the plugin failing on every
// run thereafter.
func LoadState(dir string) (map[string]*smart.Device, error) {
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]*smart.Device{}, nil
	}
	if err != nil {
		return map[string]*smart.Device{}, fmt.Errorf("read state: %w", err)
	}
	state := map[string]*smart.Device{}
	if err := json.Unmarshal(data, &state); err != nil {
		return map[string]*smart.Device{}, fmt.Errorf("state file corrupt, ignoring: %w", err)
	}
	return state, nil
}

// SaveState records the current readings for the next comparison.
//
// Readings are merged into the existing state rather than replacing it, so a
// device that was unreadable on this sweep keeps its last good reading.
func SaveState(dir string, previous map[string]*smart.Device, report *smart.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	next := make(map[string]*smart.Device, len(previous))
	for k, v := range previous {
		next[k] = v
	}
	if report != nil {
		for i := range report.Devices {
			d := report.Devices[i]
			next[DeviceKey(&d)] = &d
		}
	}

	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	// Write-then-rename, so a crash mid-write cannot leave a truncated file.
	tmp, err := os.CreateTemp(dir, StateFile+".*")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, StateFile)); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
