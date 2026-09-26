package fixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/00Webbo/tropis/pkg/schema"
)

// newReader wraps bytes for a streaming decoder.
func newReader(b []byte) io.Reader { return bytes.NewReader(b) }

// ErrNoLabel is returned when a scenario directory has no label.json.
var ErrNoLabel = errors.New("no label.json in directory")

// LoadLabel reads the ground-truth label for a scenario.
//
// This is the only function in the project that reads a label, and it exists
// solely for the scorer. Call it after verdicts have been produced, never
// before, and never from anything that feeds the reasoning layer.
//
// The blinding guarantee is not enforced by this function — it is enforced by
// LoadFixture being unable to reach a label at all. This function is the
// deliberate, single, named exception.
func LoadLabel(dir string) (*schema.Label, error) {
	path := filepath.Join(dir, LabelFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoLabel, dir)
		}
		return nil, fmt.Errorf("read label: %w", err)
	}

	var l schema.Label
	dec := json.NewDecoder(newReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("invalid label %s: %w", path, err)
	}
	return &l, nil
}

// LoadLabelFor reads the label for a case and checks it belongs to that
// case's fixture. A label paired with the wrong fixture would produce a
// confidently wrong accuracy number, which is worse than an error.
func LoadLabelFor(c Case) (*schema.Label, error) {
	l, err := LoadLabel(c.Dir)
	if err != nil {
		return nil, err
	}
	if err := l.Matches(c.Fixture); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Dir, err)
	}
	return l, nil
}
