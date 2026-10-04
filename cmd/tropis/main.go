// Command tropis is the Tropis CLI.
//
//	tropis analyze <node>      diagnose one node
//	tropis eval                replay the fixture corpus and score it
//	tropis capture             capture a fixture from a live node
//	tropis inventory ...       validate and query the rig inventory
//
// Tropis is read-only. No command changes cluster state other than writing
// Tropis's own NodeHealthReport resources.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
