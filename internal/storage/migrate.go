package storage

import (
	"errors"
	"fmt"
	app "github.com/IvanKuzyshyn/temporal-harness-demo/migrations/app"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"net/url"
)

// Migrate applies only the embedded application schema, using application-role
// credentials. down is destructive and must be explicitly requested by tooling.
func Migrate(databaseURL, direction string) (err error) {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("migration direction must be up or down")
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("expected postgres URL")
	}
	if u.User == nil || u.User.Username() != "webchat" {
		return fmt.Errorf("application migrations require webchat role")
	}
	if u.Path == "/temporal" || u.Path == "/temporal_visibility" {
		return fmt.Errorf("refusing Temporal database")
	}
	u.Scheme = "pgx5"
	source, err := iofs.New(app.Files, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, u.String())
	if err != nil {
		source.Close()
		return err
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
