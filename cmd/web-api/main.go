// web-api serves durable intent and supervises its real Temporal dispatcher.
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
	"sync/atomic"
	"syscall"
	"time"

	chatclient "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	tc "go.temporal.io/sdk/client"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func loadConfig(getenv func(string) string) (api.Config, *pgxpool.Config, error) {
	cfg := api.Config{ListenAddress: getenv("WEBCHAT_API_LISTEN"), BrowserOrigin: getenv("WEBCHAT_BROWSER_ORIGIN"), ProxySecret: getenv("WEBCHAT_PROXY_SECRET")}
	if getenv("WEBCHAT_EXECUTION_BACKEND") != "" {
		return cfg, nil, errors.New("execution backend selection is unsupported; API uses Temporal")
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
	if err = pool.QueryRow(startup, "SELECT version=5 AND NOT dirty FROM schema_migrations").Scan(&ready); err != nil || !ready {
		return errors.New("application schema unavailable; run web-api -migrate")
	}
	address := os.Getenv("WEBCHAT_TEMPORAL_ADDRESS")
	if address == "" {
		address = "127.0.0.1:7233"
	}
	// Lazy dialing lets durable intent remain available during Temporal outages.
	sdk, err := tc.NewLazyClient(tc.Options{HostPort: address})
	if err != nil {
		return errors.New("cannot initialize Temporal transport")
	}
	defer sdk.Close()
	repo := storage.New(pool)
	gateway := chatclient.NewWebGateway(sdk, repo)
	var reconciling atomic.Bool
	cfg.GatewayConnected = gateway.Connected
	cfg.WorkersPolling = gateway.WorkersPolling
	cfg.ReconciliationReady = reconciling.Load
	dispatcher, err := dispatch.New(repo, gateway, dispatch.Options{})
	if err != nil {
		return errors.New("cannot initialize dispatcher")
	}
	healthCtx, healthCancel := context.WithCancel(ctx)
	healthDone := make(chan struct{})
	go func() { defer close(healthDone); gateway.ObserveHealth(healthCtx) }()
	defer func() { healthCancel(); <-healthDone }()
	handler, err := api.NewServer(repo, cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return errors.New("cannot bind private API loopback address")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	log.Printf("private API listening on %s; durable queue and supervised Temporal dispatch enabled", cfg.ListenAddress)
	return supervise(ctx, func(runCtx context.Context) error { return serve(runCtx, server, listener) }, func(runCtx context.Context) error {
		reconciling.Store(true)
		defer reconciling.Store(false)
		return dispatcher.Run(runCtx)
	})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "web-api:", err)
		os.Exit(1)
	}
}
