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
	"syscall"
	"time"

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
	if s.RunCounter < 1 || s.PendingApproval != nil || len(s.Inbox) > 0 || len(s.ActiveTools) > 0 {
		return false
	}
	return s.Status == "idle" || s.Status == "settled" || s.Status == "failed"
}
func settlementError(s workflows.State) error {
	if s.LastRunFailure != nil {
		return fmt.Errorf("session failed: %s: %s", s.LastRunFailure.Reason, s.LastRunFailure.Detail)
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
	Type    string             `json:"type"`
	ID      string             `json:"id"`
	Status  string             `json:"status"`
	Turn    int                `json:"turn"`
	Failure *workflows.Failure `json:"failure,omitempty"`
	Answer  string             `json:"answer,omitempty"`
}

func summary(id string, s workflows.State) cliSummary {
	return cliSummary{Type: "session_state", ID: id, Status: s.Status, Turn: s.Turn, Failure: s.LastRunFailure, Answer: s.Answer}
}
func printFinal(out io.Writer, id string, s workflows.State, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(summary(id, s))
	}
	if s.Answer != "" {
		_, err := fmt.Fprintln(out, s.Answer)
		return err
	}
	return nil
}
