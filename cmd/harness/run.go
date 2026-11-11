package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
)

type runOptions struct {
	prompt, session, model, workdir string
	json, autoApprove, external     bool
	timeout                         time.Duration
}

func runCommand(args []string, out io.Writer) error {
	var o runOptions
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	f.StringVar(&o.prompt, "p", "", "prompt (never interpreted by a shell)")
	f.StringVar(&o.session, "session", "", "session ID")
	f.StringVar(&o.model, "model", "", "model override")
	f.StringVar(&o.workdir, "workdir", "", "project root (overrides HARNESS_WORKDIR; default: current directory)")
	f.BoolVar(&o.json, "json", false, "JSONL events and authoritative final session_state")
	f.BoolVar(&o.autoApprove, "auto-approve", false, "WARNING: approve current tool requests automatically")
	f.BoolVar(&o.external, "external-worker", false, "use existing dependencies and external worker")
	f.DurationVar(&o.timeout, "timeout", 5*time.Minute, "wait timeout; cancellation detaches without stopping the session")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || strings.TrimSpace(o.prompt) == "" || o.timeout <= 0 {
		return fmt.Errorf("run requires -p text and a positive timeout, without positional arguments")
	}
	cfg, err := config.LoadWithWorkdir(o.workdir)
	if err != nil {
		return err
	}
	if o.model != "" {
		cfg.Model = o.model
	}
	if o.session == "" {
		id, err := cliID()
		if err != nil {
			return err
		}
		o.session = "session-" + id
	}
	ctx, cancel := cliContext(o.timeout)
	defer cancel()
	b, closeAll, err := openCLI(ctx, cfg, true, o.external)
	if err != nil {
		return err
	}
	defer closeAll()
	return runSession(ctx, b, cfg, o, out, os.Stderr, cliPollInterval)
}

func runSession(ctx context.Context, b cliBackend, cfg config.Config, o runOptions, out, diag io.Writer, interval time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	baseline, err := b.Query(ctx, o.session)
	if err != nil {
		var missing *serviceerror.NotFound
		if !errors.As(err, &missing) {
			return fmt.Errorf("baseline query: %w", err)
		}
	}
	haveBaseline := err == nil
	requestID, err := cliID()
	if err != nil {
		return err
	}
	input := sessionInput(cfg)
	// Establish the tail cursor BEFORE Submit. Subsequent bounded reads include even
	// a run that starts and settles while the enqueue Update is returning.
	dataDir := cfg.DataDir
	if haveBaseline && baseline.Session != nil {
		// Use raw metadata, never the sanitized diagnostic or reconnecting client's root.
		dataDir = baseline.Session.DataDir
	}
	path, err := events.NewStore(dataDir).Path(o.session)
	if err != nil {
		return err
	}
	// Existing-session metadata is known before submission, even for a fast run.
	// New sessions report only after their first authoritative successful query.
	if haveBaseline {
		if err := printRunConfiguration(diag, baseline.Session, input); err != nil {
			return err
		}
	}
	configurationReported := haveBaseline
	var offset int64
	streamOK := true
	if haveBaseline && baseline.Session != nil {
		// Missing event files are normal; missing/invalid authoritative roots are not.
		// Disable optional display rather than silently switching to client storage.
		st, e := os.Stat(dataDir)
		if !filepath.IsAbs(dataDir) || e != nil || !st.IsDir() {
			fmt.Fprintln(diag, "warning: event stream unavailable: original session DataDir is invalid or unavailable; no client-directory fallback; recovering via state query")
			streamOK = false
		}
	} else if haveBaseline {
		fmt.Fprintln(diag, "note: original event source unknown (legacy metadata unavailable); using client-selected DataDir for optional events only")
	}
	if streamOK {
		if st, e := os.Stat(path); e == nil {
			offset = st.Size()
		} else if !errors.Is(e, os.ErrNotExist) {
			fmt.Fprintf(diag, "warning: event stream unavailable; recovering via state query: %v\n", e)
			streamOK = false
		}
	}
	var actor string
	if o.autoApprove {
		fmt.Fprintln(diag, "WARNING: --auto-approve enables automatic approval of tool actions for this session.")
		u, e := user.Current()
		if e != nil {
			return fmt.Errorf("approval user: %w", e)
		}
		actor = u.Username
		if actor == "" {
			return fmt.Errorf("approval user is empty")
		}
	}
	receipt, err := b.Submit(ctx, o.session, input, workflows.MessageInput{Text: o.prompt, RequestID: requestID, WhenBusy: "followUp"})
	if err != nil {
		return err
	}
	switch receipt.Disposition {
	case "started", "queued", "steered":
	default:
		return fmt.Errorf("unexpected submit disposition %q", receipt.Disposition)
	}
	encoder := json.NewEncoder(out)
	plainStreamed := false
	lastDecision := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if streamOK {
			batch, next, e := tailCLIEvents(path, offset)
			offset = next
			for _, event := range batch {
				// Terminal child events are display only, never completion signals. Even
				// a terminal parent event is not trusted without authoritative Query.
				if event.Type == events.AgentSettled && !terminalParent(event, o.session) {
					continue
				}
				if o.json {
					if err := encoder.Encode(event); err != nil {
						return err
					}
				} else if event.Type == events.MessageUpdate && event.WorkflowID == o.session && event.ParentWorkflowID == "" {
					var message events.MessageData
					if json.Unmarshal(event.Data, &message) == nil && message.Delta != "" {
						if _, err := io.WriteString(out, terminalText(message.Delta)); err != nil {
							return err
						}
						plainStreamed = true
					}
				}
			}
			if e != nil {
				fmt.Fprintf(diag, "warning: event stream unavailable; recovering via state query: %v\n", e)
				streamOK = false
			}
		}
		state, e := b.Query(ctx, o.session)
		if e != nil {
			return fmt.Errorf("session query: %w", e)
		}
		if !configurationReported {
			if err := printRunConfiguration(diag, state.Session, input); err != nil {
				return err
			}
			configurationReported = true
		}
		if runSettled(state, baseline) {
			if plainStreamed {
				if _, err := fmt.Fprintln(out); err != nil {
					return err
				}
			}
			if err := printFinal(out, o.session, state, o.json); err != nil {
				return err
			}
			return settlementError(state)
		}
		if o.autoApprove && state.PendingApproval != nil {
			p := state.PendingApproval
			key := p.RequestID + "/" + p.ActionHash
			if p.Decision == nil && key != lastDecision {
				if p.RequestID == "" || p.ActionHash == "" {
					return fmt.Errorf("invalid current approval request")
				}
				// Decide's workflow validator checks these exact current-request values;
				// never approve from a potentially stale display event.
				if err := b.Decide(ctx, o.session, workflows.DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: true, User: actor, Reason: "explicit CLI --auto-approve"}); err != nil {
					return err
				}
				lastDecision = key
			}
		}
		if err := pollPause(ctx, interval); err != nil {
			return err
		}
	}
}
