// Command gen-schema writes the generated JSON Schema for the Verdict type to
// stdout, or to the file named by -o.
//
// The schema is derived from the Go types in pkg/schema, so it cannot drift
// from them. Regenerate with `go generate ./...` and commit the result.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nathanwebb/tropis/pkg/schema"
)

func main() {
	out := flag.String("o", "", "write schema to this file instead of stdout")
	flag.Parse()

	b, err := schema.VerdictJSONSchemaBytes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-schema: %v\n", err)
		os.Exit(1)
	}

	if *out == "" {
		os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen-schema: %v\n", err)
		os.Exit(1)
	}
}
