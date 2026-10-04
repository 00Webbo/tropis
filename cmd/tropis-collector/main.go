// Command tropis-collector is the host-layer collector.
//
// It runs as a DaemonSet, one pod per node, and reads SMART data and nothing
// else. It holds no Kubernetes API permissions.
//
// Subcommands:
//
//	collect     read SMART from every device and print the report as JSON
//	prefilter   run the deterministic rules; with --npd, speak NPD's plugin
//	            protocol (exit 0/1/2, one short line on stdout)
//	serve       serve the latest report over HTTP for the analysis CronJob
//
// The prefilter subcommand is the same artifact node-problem-detector calls
// through hack/npd-plugin/, so the rules exist exactly once.
package main

import (
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
