package devcorpus

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanwebb/tropis/eval/scenarios"
	"github.com/nathanwebb/tropis/pkg/fixture"
	"github.com/nathanwebb/tropis/pkg/reason"
)

var committed = filepath.Join("..", "testdata")

// The committed corpus must be exactly what the generator produces from the
// current scenario definitions, so the two can never quietly diverge.
func TestCommittedCorpusIsCurrent(t *testing.T) {
	all, err := scenarios.Load()
	if err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	if err := Generate(fresh, all); err != nil {
		t.Fatal(err)
	}

	count := 0
	err = filepath.WalkDir(fresh, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(fresh, path)
		want, _ := os.ReadFile(path)
		got, err := os.ReadFile(filepath.Join(committed, rel))
		if err != nil {
			t.Errorf("%s is missing from eval/testdata; run go run ./hack/gen-devcorpus", rel)
			return nil
		}
		// Line endings may differ on a Windows checkout.
		if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
			t.Errorf("%s is stale; run go run ./hack/gen-devcorpus", rel)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2*2*len(all)+1 {
		t.Errorf("compared %d files, want %d", count, 2*2*len(all)+1)
	}
}

func TestCorpusLoadsAndIsNeverPublishable(t *testing.T) {
	cases, err := fixture.LoadCorpus(committed)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(cases) != 60 {
		t.Errorf("loaded %d fixtures, want 60", len(cases))
	}
	for _, c := range cases {
		if !c.Fixture.Synthetic {
			t.Errorf("%s is not marked synthetic", c.Dir)
		}
		if _, err := fixture.LoadLabelFor(c); err != nil {
			t.Errorf("%s: %v", c.Dir, err)
		}
	}
	if _, err := fixture.PublishableCorpus(committed); err == nil {
		t.Fatal("the development corpus must never load as publishable")
	}
}

// Every fixture replays through the real parser and input builder.
func TestCorpusBuildsAnalysisInputs(t *testing.T) {
	cases, err := fixture.LoadCorpus(committed)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		in, err := reason.BuildInput(reason.RequestFromFixture(c.Fixture, nil))
		if err != nil {
			t.Errorf("%s: %v", c.Dir, err)
			continue
		}
		doc, _ := in.Document()
		if len(doc.Host.Devices)+len(doc.Host.Unreadable) == 0 {
			t.Errorf("%s: no host data survived parsing", c.Dir)
		}
		if len(doc.Kubernetes.Pods) == 0 {
			t.Errorf("%s: no pods", c.Dir)
		}
	}
}

// The SMART archetypes must parse to the values their names promise, through
// the real parser.
func TestSMARTArchetypesParse(t *testing.T) {
	cases, err := fixture.LoadCorpus(committed)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]reason.Document{}
	for _, c := range cases {
		in, err := reason.BuildInput(reason.RequestFromFixture(c.Fixture, nil))
		if err != nil {
			t.Fatal(err)
		}
		byID[c.Fixture.ScenarioID], _ = in.Document()
	}

	growing := byID["degradation-postgres-gradual"].Host.Devices[0]
	if *growing.Current.ReallocatedSectors != 488 || *growing.Previous.ReallocatedSectors != 312 {
		t.Errorf("realloc-growing: %v -> %v", *growing.Previous.ReallocatedSectors, *growing.Current.ReallocatedSectors)
	}
	stable := byID["stable-defects-config-crash"].Host.Devices[0]
	if *stable.Current.ReallocatedSectors != *stable.Previous.ReallocatedSectors {
		t.Error("realloc-stable should not change between readings")
	}
	pending := byID["pending-sectors-postgres"].Host.Devices[0]
	if *pending.Current.PendingSectors != 24 || *pending.Previous.PendingSectors != 0 {
		t.Errorf("pending-new: %v -> %v", *pending.Previous.PendingSectors, *pending.Current.PendingSectors)
	}
	if len(byID["unreadable-smart-restarts"].Host.Unreadable) != 1 {
		t.Error("unreadable archetype should parse as an unreadable device")
	}
}
