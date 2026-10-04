package capture

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusAlerts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/alerts" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"alerts":[
			{"labels":{"alertname":"KubePodCrashLooping"},"state":"firing"},
			{"labels":{"alertname":"KubePodCrashLooping"},"state":"firing"},
			{"labels":{"alertname":"NodeFilesystemAlmostOutOfSpace"},"state":"pending"},
			{"labels":{"alertname":"KubeContainerWaiting"},"state":"firing"}]}}`)
	}))
	defer srv.Close()

	got, err := PrometheusAlerts(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "KubeContainerWaiting,KubePodCrashLooping" {
		t.Errorf("alerts = %v; want firing only, deduplicated, sorted", got)
	}
}
