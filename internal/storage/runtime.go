package storage

import (
	"errors"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimePoolConfig validates the effective pgx pool connection before dialing.
// No credentials/SQL/URL are included in returned errors. Unlike the migration
// test helper, the executable requires non-placeholder application credentials.
func RuntimePoolConfig(raw string) (*pgxpool.Config, error) {
	invalid := errors.New("invalid application database configuration: require loopback, webchat role, explicit application database and non-placeholder password")
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return nil, invalid
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, invalid
	}
	c := cfg.ConnConfig
	if validateApplicationIdentity(c) != nil {
		return nil, invalid
	}
	validHost := func(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }
	if !validHost(c.Host) {
		return nil, invalid
	}
	for _, f := range c.Fallbacks {
		if !validHost(f.Host) {
			return nil, invalid
		}
	}
	if len(c.Password) < 24 || len(c.Password) > 1024 {
		return nil, invalid
	}
	lower := strings.ToLower(c.Password)
	for _, placeholder := range []string{"change", "replace", "example", "placeholder", "local-"} {
		if strings.Contains(lower, placeholder) {
			return nil, invalid
		}
	}
	distinct := map[rune]bool{}
	for _, r := range c.Password {
		distinct[r] = true
	}
	if len(distinct) < 8 {
		return nil, invalid
	}
	c.ConnectTimeout = 5 * time.Second
	cfg.MaxConns = 10
	cfg.MinConns = 0
	return cfg, nil
}
