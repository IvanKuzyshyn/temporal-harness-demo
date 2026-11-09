package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
)

func sessionCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("usage: session list [--json]")
	}
	f := flag.NewFlagSet("session list", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	asJSON := f.Bool("json", false, "one bounded session summary per JSONL record")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("session list: unexpected arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := cliContext(5 * time.Minute)
	defer cancel()
	b, closeAll, err := openCLI(ctx, cfg, false, true)
	if err != nil {
		return err
	}
	defer closeAll()
	return listSessions(ctx, b, out, *asJSON)
}

// Visibility requires no custom search attributes. Sessions handles every page;
// querying each execution provides live status where visibility cannot.
func listSessions(ctx context.Context, b cliBackend, out io.Writer, asJSON bool) error {
	ids, err := b.Sessions(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := b.Query(ctx, id)
		if err != nil {
			var missing *serviceerror.NotFound
			var closed *serviceerror.FailedPrecondition
			if !errors.As(err, &missing) && !errors.As(err, &closed) {
				return fmt.Errorf("query session %s: %w", id, err)
			}
			state = workflows.State{Status: "closed/unavailable"}
		}
		item := summary(id, state)
		item.Answer = ""
		if asJSON {
			err = enc.Encode(item)
		} else {
			failure := ""
			if item.Failure != nil {
				failure = " failure=" + item.Failure.Reason
			}
			_, err = fmt.Fprintf(out, "%s\t%s\tturn=%d%s\n", id, item.Status, item.Turn, failure)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
