// Command tropis is the Tropis CLI.
//
//	tropis analyze <node>      diagnose one node
//	tropis sweep               pre-filter every node and diagnose the candidates
//	tropis eval                replay the fixture corpus and score it
//	tropis capture             capture a fixture from a live node
//	tropis inventory ...       validate and query the rig inventory
//	tropis version             print the version
//
// Tropis is read-only. No command changes cluster state other than writing
// Tropis's own NodeHealthReport resources and, when TROPIS_EVENTS opts in,
// Node Events in the default namespace.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
