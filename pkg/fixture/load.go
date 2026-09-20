// Package fixture loads captured scenarios from disk.
//
// The package is split deliberately into two files:
//
//   - load.go reads fixtures. It cannot read a label. There is no function
//     here that returns a schema.Label, and no code path from LoadFixture to
//     the label file on disk.
//   - score_access.go reads labels, and is the only file that can.
//
// This is what makes the blinding claim structural rather than a convention.
// The reasoning layer is handed a Fixture; the type system means it has
// nothing to leak. Ground truth reaches only the scorer, only after verdicts
// exist.
//
// If you find yourself wanting to load a label during analysis, that is the
// harness working as designed. The answer is no.
package fixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/nathanwebb/tropis/pkg/schema"
)

const (
	// FixtureFile is the capture file within a scenario directory.
	FixtureFile = "fixture.json"

	// LabelFile is the ground-truth file within a scenario directory. It is
	// never read by LoadFixture.
	LabelFile = "label.json"
)

// ErrNoFixture is returned when a directory contains no fixture.json.
var ErrNoFixture = errors.New("no fixture.json in directory")

// LoadFixture reads and validates the fixture in dir.
//
// It reads FixtureFile and nothing else. In particular it does not read
// LabelFile, and has no way to return its contents: the return type carries no
// ground truth.
func LoadFixture(dir string) (*schema.Fixture, error) {
	path := filepath.Join(dir, FixtureFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoFixture, dir)
		}
		return nil, fmt.Errorf("read fixture: %w", err)
	}

	var f schema.Fixture
	dec := json.NewDecoder(newReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("invalid fixture %s: %w", path, err)
	}
	return &f, nil
}

// Case pairs a loaded fixture with the directory it came from, so the scorer
// can find the matching label afterwards without the analysis path ever
// holding a path to it.
type Case struct {
	// Dir is the scenario directory.
	Dir string

	// Fixture is the capture. No label is present, by construction.
	Fixture *schema.Fixture
}

// LoadCorpus loads every fixture under root, descending into subdirectories.
//
// A directory is treated as a scenario when it contains a fixture.json.
// Results are sorted by directory path for determinism; the eval runner
// randomises order itself, and does so explicitly rather than relying on
// filesystem iteration order.
func LoadCorpus(root string) ([]Case, error) {
	var dirs []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == FixtureFile {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk corpus %s: %w", root, err)
	}

	sort.Strings(dirs)

	cases := make([]Case, 0, len(dirs))
	for _, dir := range dirs {
		f, err := LoadFixture(dir)
		if err != nil {
			return nil, err
		}
		cases = append(cases, Case{Dir: dir, Fixture: f})
	}
	return cases, nil
}

// PublishableCorpus returns the cases in root, and an error if any of them is
// synthetic.
//
// Synthetic fixtures are fine for development and are used for exactly that.
// They must never reach a published accuracy number: the numbers are the
// project's differentiator and synthetic fixtures would void them. Report
// generation calls this rather than LoadCorpus.
func PublishableCorpus(root string) ([]Case, error) {
	cases, err := LoadCorpus(root)
	if err != nil {
		return nil, err
	}

	var synthetic []string
	for _, c := range cases {
		if c.Fixture.Synthetic {
			synthetic = append(synthetic, c.Fixture.ScenarioID)
		}
	}
	if len(synthetic) > 0 {
		return nil, fmt.Errorf(
			"corpus contains %d synthetic fixture(s) and cannot be published: %v",
			len(synthetic), synthetic)
	}
	return cases, nil
}
