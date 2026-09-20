// Command smoke runs the SMART collector against real devices and prints what
// it found. It exists to exercise the collector against actual hardware, which
// unit tests against captured samples cannot do.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nathanwebb/tropis/pkg/host/smart"
)

func main() {
	ctx := context.Background()
	c := smart.NewCollector()

	if err := c.Available(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "smartctl unavailable: %v\n", err)
		os.Exit(1)
	}

	var report *smart.Report
	var err error
	if len(os.Args) > 1 {
		report, err = c.Collect(ctx, os.Args[1:])
	} else {
		report, err = c.CollectAll(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "collect: %v\n", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\nread %d device(s), %d failure(s)\n",
		len(report.Devices), len(report.Failures))
}
