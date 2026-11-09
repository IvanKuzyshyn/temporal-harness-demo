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
	harnessworker "github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
)

func workerCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(out)
	model := flags.String("model", "", "override HARNESS_MODEL")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("worker: unexpected arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *model != "" {
		cfg.Model = *model
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runner, err := harnessworker.Start(ctx, cfg)
	if err != nil {
		return err
	}
	defer runner.Close()
	fmt.Fprintln(out, "Worker polling agents, llm, subagents; Ctrl+C stops this worker, not persisted workflows.")
	<-ctx.Done()
	return nil
}
