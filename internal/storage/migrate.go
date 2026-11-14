package storage

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	app "github.com/IvanKuzyshyn/temporal-harness-demo/migrations/app"
	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Migrate applies only the embedded application schema, using application-role
// credentials. down is destructive and must be explicitly requested by tooling.
func Migrate(databaseURL, direction string) (err error) {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("migration direction must be up or down")
	}
	config, err := migrationConfig(databaseURL)
	if err != nil {
		return err
	}
	// Never hand the URI to another parser after validation: query alias order
	// and environment/service defaults must not change the selected identity.
	// WithInstance can create schema_migrations, so validation precedes it too.
	db := stdlib.OpenDB(*config)
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{DatabaseName: config.Database})
	if err != nil {
		return errors.Join(err, db.Close())
	}
	source, err := iofs.New(app.Files, ".")
	if err != nil {
		return errors.Join(err, driver.Close())
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return errors.Join(err, source.Close(), driver.Close())
	}
	defer func() { s, d := m.Close(); err = errors.Join(err, s, d) }()
	if direction == "up" {
		err = m.Up()
	} else {
		err = m.Down()
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// migrationConfig resolves URL, environment and service settings without opening
// a connection. Its returned configuration is the one used by the connector.
func migrationConfig(databaseURL string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, fmt.Errorf("expected postgres URL")
	}
	if u.User == nil || u.User.Username() != "webchat" {
		return nil, fmt.Errorf("application migrations require webchat role")
	}
	if u.Path == "/temporal" || u.Path == "/temporal_visibility" {
		return nil, fmt.Errorf("refusing Temporal database")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if config.User != "webchat" {
		return nil, fmt.Errorf("application migrations require effective webchat role")
	}
	if config.Database == "" || config.Database == "temporal" || config.Database == "temporal_visibility" {
		return nil, fmt.Errorf("application migrations require an explicit non-Temporal database")
	}
	// Startup GUCs can change the session role independently of Config.User.
	// Do not interpret the server's options grammar here; reject that escape
	// hatch, including settings inherited from PGOPTIONS or a service file.
	for key := range config.RuntimeParams {
		key = strings.ToLower(key)
		if key == "role" || key == "session_authorization" || key == "options" || strings.HasPrefix(key, "x-") {
			return nil, fmt.Errorf("unsupported application migration parameter %q", key)
		}
	}
	return config, nil
}
