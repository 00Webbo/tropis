// Command gen-devcorpus regenerates the synthetic development corpus in
// eval/testdata from eval/scenarios/scenarios.yaml.
//
// Everything it writes is marked synthetic and is never publishable. The
// output directory is fixed, not a flag: Generate replaces the directory it
// is given, and pointed at eval/fixtures it would delete the real corpus and
// put synthetic fixtures in its place.
package main

import (
	"fmt"
	"os"

	"github.com/I-Am-Webbo/tropis/eval/devcorpus"
	"github.com/I-Am-Webbo/tropis/eval/scenarios"
)

const out = "eval/testdata"

func main() {
	all, err := scenarios.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-devcorpus: %v\n", err)
		os.Exit(1)
	}
	if err := devcorpus.Generate(out, all); err != nil {
		fmt.Fprintf(os.Stderr, "gen-devcorpus: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d synthetic fixtures (%d scenarios x 2 NPD variants) to %s\n", 2*len(all), len(all), out)
}
