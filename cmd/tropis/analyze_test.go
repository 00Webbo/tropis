package main

import (
	"flag"
	"io"
	"testing"

	"github.com/I-Am-Webbo/tropis/pkg/k8s"
)

// The chart sets the collector's listen port; the analyser must dial the
// same one, from the environment or the flag.
func TestCollectorPortReachesHostFetcher(t *testing.T) {
	parse := func(args ...string) *k8s.HostFetcher {
		t.Helper()
		var cf clusterFlags
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		cf.register(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return cf.hostFetcher(nil)
	}

	if got := parse().Port; got != k8s.DefaultCollectorPort {
		t.Errorf("default port = %d, want %d", got, k8s.DefaultCollectorPort)
	}
	t.Setenv("TROPIS_COLLECTOR_PORT", "9500")
	if got := parse().Port; got != 9500 {
		t.Errorf("port from env = %d, want 9500", got)
	}
	if got := parse("--collector-port", "9600").Port; got != 9600 {
		t.Errorf("port from flag = %d, want 9600", got)
	}
}
