//go:build integration

package storage_test

import (
	"os"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	app "github.com/IvanKuzyshyn/temporal-harness-demo/migrations/app"
	"github.com/stretchr/testify/require"
)

// Apply the immutable old schema, write real content, then upgrade to 000002:
// an empty up/down/up alone would not catch broken backfill or lost identities.
func TestMigrationDispatchUpgrade(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PHASE") != "migration" {
		t.Skip("destructive migration cycle only in runner's isolated migration phase")
	}
	r, p := database(t)
	raw := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	require.NoError(t, storage.Migrate(raw, "up"))
	require.NoError(t, storage.Migrate(raw, "down"))
	original, e := app.Files.ReadFile("000001_chat.up.sql")
	require.NoError(t, e)
	_, e = p.Exec(ctx, string(original))
	require.NoError(t, e)
	_, e = p.Exec(ctx, "INSERT INTO schema_migrations(version,dirty) VALUES(1,false)")
	require.NoError(t, e)
	c := conversation(t, r)
	a, e := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, e)
	require.NoError(t, storage.Migrate(raw, "up"))
	got, e := r.GetCommand(ctx, c.ID, a.CommandID)
	require.NoError(t, e)
	require.Equal(t, a.CommandID, got.CommandID)
	require.Equal(t, a.TurnID, got.TurnID)
	require.Equal(t, 1, count(t, p, "messages", c.ID))
	claims, e := r.ClaimCommands(ctx, 10, time.Second)
	require.NoError(t, e)
	require.Len(t, claims, 1)
	require.Equal(t, a.CommandID, claims[0].Command.Receipt.CommandID)
	require.EqualValues(t, 1, claims[0].Sequence)
	page, e := r.ListConversations(ctx, "", 10)
	require.NoError(t, e)
	require.Len(t, page.Conversations, 1)
	require.Equal(t, c.ID, page.Conversations[0].ID)
}
