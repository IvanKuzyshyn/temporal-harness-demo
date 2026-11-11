package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func waitCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("wait", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	until := f.String("until", "settled", "settled or blocked")
	timeout := f.Duration("timeout", 5*time.Minute, "wait timeout; cancellation detaches without Stop")
	// Go's flag parser stops at the first positional: support the documented
	// wait <session> --until ... form as well as flags before the ID.
	var id string
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		id = args[0]
		args = args[1:]
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if id == "" && f.NArg() == 1 {
		id = f.Arg(0)
	} else if f.NArg() != 0 {
		return fmt.Errorf("wait requires exactly one session ID")
	}
	if id == "" || *timeout <= 0 || (*until != "settled" && *until != "blocked") {
		return fmt.Errorf("wait requires a session ID, --until settled|blocked, and positive --timeout")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := cliContext(*timeout)
	defer cancel()
	b, closeAll, err := openCLI(ctx, cfg, false, true)
	if err != nil {
		return err
	}
	defer closeAll()
	state, err := pollSession(ctx, b, id, *until, cliPollInterval)
	if err != nil {
		return err
	}
	if err := printFinal(out, id, state, false); err != nil {
		return err
	}
	if *until == "settled" {
		return settlementError(state)
	}
	_, err = fmt.Fprintf(out, "%s blocked: approval %s\n", id, state.PendingApproval.RequestID)
	return err
}
func pollSession(ctx context.Context, b cliBackend, id, until string, interval time.Duration) (workflows.State, error) {
	if until != "settled" && until != "blocked" {
		return workflows.State{}, fmt.Errorf("unknown wait condition %q", until)
	}
	for {
		if err := ctx.Err(); err != nil {
			return workflows.State{}, err
		}
		state, err := b.Query(ctx, id)
		if err != nil {
			return state, err
		}
		if (until == "blocked" && state.PendingApproval != nil && state.PendingApproval.Decision == nil) || (until == "settled" && sessionSettled(state)) {
			return state, nil
		}
		if err := pollPause(ctx, interval); err != nil {
			return state, err
		}
	}
}
