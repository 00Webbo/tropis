package deploy

import (
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
	want := map[string]bool{"$.version": false, "$.appVersion": false}
	for _, f := range root.ExtraFiles {
		if f.Path == "deploy/helm/tropis/Chart.yaml" && f.Type == "yaml" {
			if _, ok := want[f.JSONPath]; ok {
				want[f.JSONPath] = true
			}
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("release-please does not bump Chart.yaml %s", path)
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
