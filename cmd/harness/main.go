// Command harness demonstrates durable local agents with Temporal.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := execute(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" || args[0] == "-h" {
		_, err := fmt.Fprintln(out, "Usage: harness <command>\nCommands: demo tui worker run doctor session approve deny wait")
		return err
	}
	switch args[0] {
	case "doctor":
		return doctor(args[1:], out)
	case "demo", "tui", "worker", "run", "session", "approve", "deny", "wait":
		return fmt.Errorf("%s: not implemented at this scaffold stack position", args[0])
	default:
		return fmt.Errorf("unknown command %q (use --help)", args[0])
	}
}
