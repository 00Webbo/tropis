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
