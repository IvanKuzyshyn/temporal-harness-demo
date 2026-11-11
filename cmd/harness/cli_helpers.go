package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	hc "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/devenv"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	hw "github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

const cliPollInterval = 100 * time.Millisecond

type cliBackend interface {
	Query(context.Context, string) (workflows.State, error)
	Submit(context.Context, string, workflows.Input, workflows.MessageInput) (workflows.MessageReceipt, error)
	Decide(context.Context, string, workflows.DecisionInput) error
	Sessions(context.Context) ([]string, error)
}

// Cancellation detaches only: no Stop update is sent to the durable session.
func cliContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return ctx, func() { cancel(); stop() }
}

func openCLI(ctx context.Context, cfg config.Config, setup, external bool) (cliBackend, func(), error) {
	var cleanups []func()
	closeAll := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	if setup && !external {
		deps := devenv.New(cfg)
		cleanups = append(cleanups, func() { _ = deps.Close() })
		if err := deps.Check(ctx, true, os.Stderr); err != nil {
			closeAll()
			return nil, func() {}, err
		}
		runner, err := hw.Start(ctx, cfg)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		cleanups = append(cleanups, func() { _ = runner.Close() })
	}
	c, err := hc.Dial(ctx, cfg.TemporalAddress)
	if err != nil {
		closeAll()
		return nil, func() {}, err
	}
	cleanups = append(cleanups, c.Close)
	return c, closeAll, nil
}
func cliID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func terminalParent(e events.Event, id string) bool {
	return e.Type == events.AgentSettled && e.WorkflowID == id && e.ParentWorkflowID == ""
}
func sessionSettled(s workflows.State) bool {
	if s.RunCounter < 1 || s.PendingApproval != nil || len(s.ActiveTools) > 0 {
		return false
	}
	// Match the workflow's automatic-work contract. Failure metadata survives
	// settlement yields, during which genuine new input can resume the queue.
	if !s.QueuePaused && (s.ContinueRequested || len(s.Inbox) > 0) {
		return false
	}
	return s.Status == "idle" || s.Status == "settled" || s.Status == "failed"
}
func sessionFailed(s workflows.State) bool {
	return s.LastRunFailure != nil || s.Status == "failed"
}

// Inbox entries can be steers consumed by the current run, not new runs.
// Success needs at least baseline+1 and authoritative quiescence.
func runSettled(s, baseline workflows.State) bool {
	if !sessionSettled(s) || s.RunCounter < baseline.RunCounter {
		return false
	}
	if sessionFailed(s) {
		// A new run may fail while retaining our request. Also recognize failure
		// of the run already active at Submit, without a counter increment.
		// Do not report a preexisting idle failure while Submit is being drained.
		changedFailure := (s.LastRunFailure == nil) != (baseline.LastRunFailure == nil)
		if s.LastRunFailure != nil && baseline.LastRunFailure != nil {
			changedFailure = *s.LastRunFailure != *baseline.LastRunFailure
		}
		baselineActive := baseline.Status == "working" || baseline.Status == "blocked"
		return s.RunCounter > baseline.RunCounter || baselineActive || changedFailure ||
			(s.Status == "failed" && !sessionFailed(baseline))
	}
	return s.RunCounter > baseline.RunCounter
}
func settlementError(s workflows.State) error {
	if s.LastRunFailure != nil {
		return fmt.Errorf("session failed: %s: %s", terminalText(s.LastRunFailure.Reason), terminalText(s.LastRunFailure.Detail))
	}
	if s.Status == "failed" {
		return fmt.Errorf("session failed")
	}
	return nil
}
func pollPause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Emit a bounded summary rather than the potentially large inbox/tool state.
type cliSummary struct {
	Session *workflows.SessionInfo `json:"session,omitempty"`
	Type    string                 `json:"type"`
	ID      string                 `json:"id"`
	Status  string                 `json:"status"`
	Turn    int                    `json:"turn"`
	Failure *workflows.Failure     `json:"failure,omitempty"`
	Answer  string                 `json:"answer,omitempty"`
}

func summary(id string, s workflows.State) cliSummary {
	return cliSummary{Type: "session_state", ID: id, Status: s.Status, Turn: s.Turn, Failure: s.LastRunFailure, Answer: s.Answer, Session: s.Session}
}

// terminalText removes terminal control introducers (including C1), not just
// complete escape sequences: split streamed sequences must be harmless too.
// Preserve ordinary multiline/tabbed model and tool text. JSON uses its encoder.
func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// Diagnostics always go to stderr, including in JSONL mode. Quote sanitized
// metadata to keep paths/models on one line and prevent control injection.
func printRunConfiguration(diag io.Writer, s *workflows.SessionInfo, selected workflows.Input) error {
	if s == nil {
		_, err := fmt.Fprintln(diag, "configuration: unknown (legacy session metadata unavailable); client selection is not authoritative")
		return err
	}
	if _, err := fmt.Fprintf(diag, "configuration: original session model=%q root=%q dataDir=%q scenario=%q\n", terminalText(s.Model), terminalText(s.Workdir), terminalText(s.DataDir), terminalText(s.Scenario)); err != nil {
		return err
	}
	if s.Model != selected.Model || s.Workdir != selected.Workdir || s.DataDir != selected.DataDir || (selected.Scenario != "" && s.Scenario != selected.Scenario) {
		_, err := fmt.Fprintln(diag, "configuration mismatch: client selection not applied; using original session configuration")
		return err
	}
	return nil
}

func printFinal(out io.Writer, id string, s workflows.State, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(summary(id, s))
	}
	if s.Answer != "" {
		_, err := fmt.Fprintln(out, terminalText(s.Answer))
		return err
	}
	return nil
}
