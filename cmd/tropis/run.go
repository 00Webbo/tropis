package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 64
)

type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

var commands = map[string]command{
	"inventory": {"validate and query the rig inventory", runInventory},
}

func usage() string {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("usage: tropis <command> [flags]\n\ncommands:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %-10s  %s\n", n, commands[n].summary)
	}
	return b.String()
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage())
		return exitOK
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "tropis: unknown command %q\n\n%s", args[0], usage())
		return exitUsage
	}
	return cmd.run(args[1:], stdout, stderr)
}

func fail(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "tropis: "+format+"\n", a...)
	return exitError
}
