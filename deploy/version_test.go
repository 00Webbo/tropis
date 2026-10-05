package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"
)

var semver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// The chart's version is the release version: merging a new one to main
// publishes it. The image tag comes from appVersion, so the two must be the
// same number, or a release would point its chart at an image that was never
// pushed.
func TestChartVersionIsTheReleaseVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("helm", "tropis", "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Version    string `json:"version"`
		AppVersion string `json:"appVersion"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	if !semver.MatchString(chart.Version) {
		t.Errorf("chart version %q is not semver (no leading v)", chart.Version)
	}
	if chart.AppVersion != chart.Version {
		t.Errorf("appVersion %q must equal version %q: one number names the release, its image and its chart",
			chart.AppVersion, chart.Version)
	}
}

// release-please bumps the release version. Its config must keep pointing
// at both of the chart's version fields, or a release would tag one number
// and ship a chart and image carrying another.
func TestReleasePleaseBumpsTheChart(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "release-please-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Packages map[string]struct {
			ReleaseType    string `json:"release-type"`
			InitialVersion string `json:"initial-version"`
			ExtraFiles     []struct {
				Type     string `json:"type"`
				Path     string `json:"path"`
				JSONPath string `json:"jsonpath"`
			} `json:"extra-files"`
		} `json:"packages"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("release-please-config.json: %v", err)
	}
	root, ok := cfg.Packages["."]
	if !ok {
		t.Fatal("release-please must manage the repository root as one package")
	}
	// The generic updater rewrites only the version on lines carrying the
	// marker, leaving the rest of Chart.yaml byte for byte. The yaml updater
	// re-serialises the whole file, dropping the quotes Helm recommends on
	// appVersion and reflowing the description.
	var generic bool
	for _, f := range root.ExtraFiles {
		if f.Path == "deploy/helm/tropis/Chart.yaml" {
			if f.Type != "generic" {
				t.Errorf("Chart.yaml must use the generic updater, not %q, which rewrites the whole file", f.Type)
			}
			generic = true
		}
	}
	if !generic {
		t.Error("release-please does not update deploy/helm/tropis/Chart.yaml")
	}
	chart, err := os.ReadFile(filepath.Join("helm", "tropis", "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "appVersion"} {
		line := regexp.MustCompile(`(?m)^` + field + `: .*$`).Find(chart)
		if !bytes.Contains(line, []byte("# x-release-please-version")) {
			t.Errorf("Chart.yaml %s line lacks the # x-release-please-version marker, so releases would not bump it: %q", field, line)
		}
	}
	if !semver.MatchString(root.InitialVersion) {
		t.Errorf("initial-version %q is not semver", root.InitialVersion)
	}

	manifest, err := os.ReadFile(filepath.Join("..", ".release-please-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(manifest, &m); err != nil || !semver.MatchString(m["."]) {
		t.Errorf(".release-please-manifest.json must map \".\" to a version: %v %v", m, err)
	}
}
