package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/nathanwebb/tropis/pkg/inventory"
)

const inventoryUsage = `usage:
  tropis inventory validate <file>
  tropis inventory target --inventory <file> --node <name> --disk <id> [--field target|device|mountpoint|transport|address]
`

func runInventory(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, inventoryUsage)
		return exitUsage
	}
	switch args[0] {
	case "validate":
		if len(args) != 2 {
			fmt.Fprint(stderr, inventoryUsage)
			return exitUsage
		}
		inv, err := inventory.Load(args[1])
		if err != nil {
			return fail(stderr, "%v", err)
		}
		destructible := 0
		for _, n := range inv.Nodes {
			for _, d := range n.Disks {
				if d.Destructible {
					destructible++
				}
			}
		}
		fmt.Fprintf(stdout, "inventory OK: cluster %s, %d nodes, %d destructible disk%s\n",
			inv.Cluster.Name, len(inv.Nodes), destructible, plural(destructible))
		return exitOK

	case "target":
		fs := flag.NewFlagSet("inventory target", flag.ContinueOnError)
		fs.SetOutput(stderr)
		file := fs.String("inventory", "", "inventory file")
		node := fs.String("node", "", "node name")
		disk := fs.String("disk", "", "disk id")
		field := fs.String("field", "target", "what to print: target, device, mountpoint, transport or address")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *file == "" || *node == "" || *disk == "" {
			fmt.Fprint(stderr, inventoryUsage)
			return exitUsage
		}
		inv, err := inventory.Load(*file)
		if err != nil {
			return fail(stderr, "%v", err)
		}
		// InjectionTarget is the one place the destructible check lives.
		n, d, err := inv.InjectionTarget(*node, *disk)
		if err != nil {
			return fail(stderr, "%v", err)
		}
		values := map[string]string{
			"target":     d.Target(),
			"device":     d.Device,
			"mountpoint": d.Mountpoint,
			"transport":  d.Transport,
			"address":    n.Address,
		}
		v, ok := values[*field]
		if !ok {
			return fail(stderr, "unknown field %q", *field)
		}
		fmt.Fprintln(stdout, v)
		return exitOK

	default:
		fmt.Fprint(stderr, inventoryUsage)
		return exitUsage
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
