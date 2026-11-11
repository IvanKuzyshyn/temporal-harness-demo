package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These regressions inspect the connection config before any driver/metadata
// construction. They require no socket, Postgres service, or migration fixture.
func cleanMigrationEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			t.Setenv(key, "")
		}
	}
}

func TestMigrationConfigRejectsEffectiveTarget(t *testing.T) {
	cleanMigrationEnv(t)
	const base = "postgres://webchat:unused@127.0.0.1:1/webchat?sslmode=disable&"
	for _, query := range []string{
		"user=temporal",
		"user=temporal&password=valid-temporal-password&dbname=temporal",
		"dbname=temporal", "database=temporal", "dbname=temporal_visibility", "database=temporal_visibility",
		"user=webchat&user=temporal", "user=temporal&user=temporal",
		"dbname=webchat&dbname=temporal", "database=webchat&database=temporal",
		"dbname=webchat&database=temporal", "database=webchat&dbname=temporal",
		"%75ser=temporal", "%64bname=temporal", "database=%74emporal",
		"database= temporal ", // pgx trims literal URI spaces, not percent-encoded spaces
		"database=",           // no implicit server-side database selection
		"role=temporal", "ROLE=temporal", "session_authorization=temporal", "options=-c%20role%3Dtemporal",
		"x-migrations-table=redirected", // the embedded helper uses fixed migration settings
	} {
		t.Run(query, func(t *testing.T) {
			cfg, err := migrationConfig(base + query)
			require.Error(t, err, "effective unsafe target must be rejected before opening a driver")
			require.Nil(t, cfg)
		})
	}
}

func TestMigrationConfigOrdinaryURLsAndResolvedAliases(t *testing.T) {
	cleanMigrationEnv(t)
	for _, tc := range []struct{ url, db string }{
		{"postgres://webchat:secret@127.0.0.1:15432/webchat?sslmode=disable", "webchat"},
		{"postgresql://webchat:secret@localhost:15432/webchat?sslmode=disable&connect_timeout=2&application_name=migrations", "webchat"},
		{"postgres://webchat:secret@127.0.0.1:15432/webchat_test_abc?sslmode=disable", "webchat_test_abc"},
		{"postgres://webchat:secret@127.0.0.1:15432/webchat?sslmode=disable&user=temporal&user=webchat", "webchat"},
		// pgx consumes aliases in input order. Re-encoding through url.Values before
		// connecting would sort database before dbname and select the forbidden DB.
		{"postgres://webchat:secret@127.0.0.1:15432/webchat?sslmode=disable&dbname=temporal&database=webchat", "webchat"},
		{"postgres://webchat:secret@127.0.0.1:15432/webchat?sslmode=disable&database=temporal&dbname=webchat", "webchat"},
	} {
		t.Run(tc.url, func(t *testing.T) {
			cfg, err := migrationConfig(tc.url)
			require.NoError(t, err)
			require.Equal(t, "webchat", cfg.User)
			require.Equal(t, tc.db, cfg.Database)
		})
	}
}

func TestMigrationConfigEnvironmentAndService(t *testing.T) {
	cleanMigrationEnv(t)
	serviceFile := filepath.Join(t.TempDir(), "pg_service.conf")
	require.NoError(t, os.WriteFile(serviceFile, []byte("[forbidden]\nuser=temporal\ndbname=temporal\n[visibility]\ndbname=temporal_visibility\n[session]\noptions=-c role=temporal\n"), 0600))
	t.Setenv("PGUSER", "temporal")
	t.Setenv("PGDATABASE", "temporal")
	t.Setenv("PGSERVICEFILE", serviceFile)
	const explicit = "postgres://webchat:secret@127.0.0.1:1/webchat?sslmode=disable"
	const noDB = "postgres://webchat:secret@127.0.0.1:1/?sslmode=disable"
	cfg, err := migrationConfig(explicit)
	require.NoError(t, err)
	require.Equal(t, "webchat", cfg.User)
	require.Equal(t, "webchat", cfg.Database)
	cfg, err = migrationConfig(noDB)
	require.Error(t, err)
	require.Nil(t, cfg, "PGDATABASE cannot silently select Temporal")
	for _, service := range []string{"forbidden", "visibility"} {
		t.Run(service, func(t *testing.T) {
			t.Setenv("PGSERVICE", service)
			cfg, err := migrationConfig(noDB)
			require.Error(t, err)
			require.Nil(t, cfg)
			// Explicit URL identity wins over env/service identity, but is validated
			// after that merge and must be passed unchanged to the actual connector.
			cfg, err = migrationConfig(explicit)
			require.NoError(t, err)
			require.Equal(t, "webchat", cfg.User)
			require.Equal(t, "webchat", cfg.Database)
		})
	}
	cfg, err = migrationConfig(noDB + "&service=visibility&servicefile=" + serviceFile)
	require.Error(t, err)
	require.Nil(t, cfg)
	t.Setenv("PGOPTIONS", "-c role=temporal")
	cfg, err = migrationConfig(explicit)
	require.Error(t, err)
	require.Nil(t, cfg)
	t.Setenv("PGOPTIONS", "")
	cfg, err = migrationConfig(explicit + "&service=session&servicefile=" + serviceFile)
	require.Error(t, err)
	require.Nil(t, cfg)
}
