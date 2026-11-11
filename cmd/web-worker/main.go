// web-worker runs only the database-backed text chat runtime.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

func run() error {
	testMock := flag.Bool("test-mock", false, "explicit deterministic model, only with a webchat_test_ database")
	flag.Parse()
	cfg, err := storage.RuntimePoolConfig(os.Getenv("WEBCHAT_DATABASE_URL"))
	if err != nil {
		return err
	}
	var model llm.Model
	if *testMock {
		if !strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_") {
			return errors.New("test mock requires isolated test database")
		}
		model = mock.New()
	} else {
		host := os.Getenv("WEBCHAT_OLLAMA_URL")
		if host == "" {
			host = "http://127.0.0.1:11434"
		}
		model, err = ollama.New(host)
		if err != nil {
			return errors.New("invalid Ollama URL")
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	pool, err := pgxpool.NewWithConfig(startup, cfg)
	if err != nil {
		return errors.New("database initialization failed")
	}
	defer pool.Close()
	var ready bool
	if err = pool.QueryRow(startup, "SELECT version=3 AND NOT dirty FROM schema_migrations").Scan(&ready); err != nil || !ready {
		return errors.New("schema unavailable; run web-api -migrate")
	}
	address := os.Getenv("WEBCHAT_TEMPORAL_ADDRESS")
	if address == "" {
		address = "127.0.0.1:7233"
	}
	// Worker lifecycle must not be tied to an expiring startup timeout.
	runner, err := worker.StartWeb(ctx, address, &activities.ChatActivities{Repository: storage.New(pool), Model: model})
	if err != nil {
		return errors.New("Temporal worker startup failed")
	}
	defer runner.Close()
	<-ctx.Done()
	return runner.Close()
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "web-worker:", err)
		os.Exit(1)
	}
}
