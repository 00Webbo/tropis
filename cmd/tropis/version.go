package main

import (
	"fmt"
	"io"

	"github.com/nathanwebb/tropis/pkg/reason"
)

func init() {
	commands["version"] = command{"print the version", func(_ []string, stdout, _ io.Writer) int {
		fmt.Fprintf(stdout, "tropis %s (prompt %s)\n", agentVersion(), reason.PromptVersion)
		return exitOK
	}}
}

// agentVersion is set at build time through reason.AgentVersion.
func agentVersion() string { return reason.AgentVersion }
