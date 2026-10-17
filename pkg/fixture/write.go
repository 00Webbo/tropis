package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/I-Am-Webbo/tropis/pkg/schema"
)

// Dir is where a scenario variant's fixture lives under a corpus root.
func Dir(root, scenario string, variant schema.Variant) string {
	return filepath.Join(root, scenario+"-"+string(variant))
}

// Write stores a fixture and its label in dir, as FixtureFile and LabelFile.
func Write(dir string, f *schema.Fixture, l *schema.Label) error {
	if err := writeJSON(filepath.Join(dir, FixtureFile), f); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, LabelFile), l)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
