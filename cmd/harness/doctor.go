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

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/devenv"
)

func doctor(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(out)
	fix := flags.Bool("fix", false, "start missing local dependencies and pull the model")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("doctor: unexpected arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Standalone setup intentionally leaves owned services alive on success.
	// Do not cancel a successful lifetime context: the manager observes it.
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	defer close(done)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-done:
		}
	}()
	manager := devenv.New(cfg)
	if err := manager.Check(ctx, *fix, out); err != nil {
		cancel()
		_ = manager.Close()
		return err
	}
	return nil
}
