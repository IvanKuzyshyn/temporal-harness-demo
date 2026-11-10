package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	hc "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/devenv"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tui"
	hw "github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func sessionInput(cfg config.Config) workflows.Input {
	return workflows.Input{Model: cfg.Model, Workdir: cfg.Workdir, DataDir: cfg.DataDir, MaxTurns: cfg.MaxTurns, ApprovalTimeout: cfg.ApprovalTimeout, HangTimeout: cfg.HangTimeout, ContinueAfterTurns: 100}
}

func interactive(args []string, out io.Writer, demo bool) error {
	name := "tui"
	if demo {
		name = "demo"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(out)
	session := flags.String("session", "", "reattach to an existing session")
	model := flags.String("model", "", "override HARNESS_MODEL")
	workdir := flags.String("workdir", "", "project root (overrides HARNESS_WORKDIR; default: current directory)")
	autoplay := flags.Bool("autoplay", false, "start guided scenario automatically (approvals remain manual)")
	external := flags.Bool("external-worker", false, "use a separate harness worker, for durability demonstrations")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s: unexpected arguments", name)
	}
	cfg, err := config.LoadWithWorkdir(*workdir)
	if err != nil {
		return err
	}
	if *model != "" {
		cfg.Model = *model
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	deps := devenv.New(cfg)
	defer deps.Close()
	if err := deps.Check(ctx, true, os.Stderr); err != nil {
		return err
	}
	if !*external {
		runner, err := hw.Start(ctx, cfg)
		if err != nil {
			return err
		}
		defer runner.Close()
	}
	client, err := hc.Dial(ctx, cfg.TemporalAddress)
	if err != nil {
		return err
	}
	defer client.Close()
	return runInteractiveUI(ctx, client, tui.Options{SessionID: *session, Model: cfg.Model, DataDir: cfg.DataDir, Input: sessionInput(cfg), Demo: demo, Autoplay: *autoplay}, func(modelUI *tui.Model) error {
		_, err := tea.NewProgram(modelUI, tea.WithContext(ctx), tea.WithOutput(out)).Run()
		return err
	})
}

// Keep disposal inside the UI lifetime so it precedes the caller's client.Close
// on every Run exit, including initialization errors and external cancellation.
func runInteractiveUI(ctx context.Context, backend tui.Backend, opts tui.Options, run func(*tui.Model) error) error {
	opts.Context = ctx
	modelUI := tui.New(backend, opts)
	defer modelUI.Close()
	err := run(modelUI)
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}
