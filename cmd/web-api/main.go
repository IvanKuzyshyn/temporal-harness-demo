// web-api serves the private, DB-backed intent API. Task4 supplies production
// gateway wiring; there is deliberately no fake backend flag or environment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func loadConfig(getenv func(string) string) (api.Config, *pgxpool.Config, error) {
	cfg := api.Config{ListenAddress: getenv("WEBCHAT_API_LISTEN"), BrowserOrigin: getenv("WEBCHAT_BROWSER_ORIGIN"), ProxySecret: getenv("WEBCHAT_PROXY_SECRET")}
	if getenv("WEBCHAT_EXECUTION_BACKEND") != "" {
		return cfg, nil, errors.New("execution backend selection is unsupported; no production gateway is wired yet")
	}
	if _, err := api.NewServer(nil, cfg); err != nil {
		return cfg, nil, err
	}
	pool, err := storage.RuntimePoolConfig(getenv("WEBCHAT_DATABASE_URL"))
	return cfg, pool, err
}
func serve(ctx context.Context, s *http.Server, listener net.Listener) error {
	done := make(chan error, 1)
	go func() { done <- s.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(shutdown); err != nil {
			_ = s.Close()
			<-done
			return errors.New("HTTP shutdown deadline exceeded")
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "apply application migrations and exit (does not start HTTP)")
	flag.Parse()
	cfg, poolCfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	if *migrate {
		if err := storage.Migrate(os.Getenv("WEBCHAT_DATABASE_URL"), "up"); err != nil {
			return errors.New("application migration failed; check database configuration and migration state")
		}
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	pool, err := pgxpool.NewWithConfig(startup, poolCfg)
	if err != nil {
		return errors.New("cannot initialize application database")
	}
	defer pool.Close()
	if err = pool.Ping(startup); err != nil {
		return errors.New("application database unavailable")
	}
	var ready bool
	if err = pool.QueryRow(startup, "SELECT version=2 AND NOT dirty FROM schema_migrations").Scan(&ready); err != nil || !ready {
		return errors.New("application schema unavailable; run web-api -migrate")
	}
	handler, err := api.NewServer(storage.New(pool), cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return errors.New("cannot bind private API loopback address")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	log.Printf("private API listening on %s; durable queue enabled; dispatcher unavailable (no gateway connected)", cfg.ListenAddress)
	return serve(ctx, server, listener)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "web-api:", err)
		os.Exit(1)
	}
}
