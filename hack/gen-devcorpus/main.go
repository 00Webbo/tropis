// Command gen-devcorpus regenerates the synthetic development corpus in
// eval/testdata from eval/scenarios/scenarios.yaml.
//
// Everything it writes is marked synthetic and is never publishable.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/00Webbo/tropis/eval/devcorpus"
	"github.com/00Webbo/tropis/eval/scenarios"
)

func main() {
	out := flag.String("o", "eval/testdata", "output directory")
	flag.Parse()

	all, err := scenarios.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-devcorpus: %v\n", err)
		os.Exit(1)
	}
	if err := devcorpus.Generate(*out, all); err != nil {
		fmt.Fprintf(os.Stderr, "gen-devcorpus: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d synthetic fixtures (%d scenarios x 2 NPD variants) to %s\n", 2*len(all), len(all), *out)
}
