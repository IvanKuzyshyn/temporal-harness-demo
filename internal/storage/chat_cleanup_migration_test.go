//go:build integration

package storage_test

import (
	"os"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	app "github.com/IvanKuzyshyn/temporal-harness-demo/migrations/app"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMigrationChatCleanupUpgrade(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PHASE") != "migration" {
		t.Skip("destructive upgrade only in owned migration phase")
	}
	r, p := database(t)
	raw := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	require.NoError(t, storage.Migrate(raw, "down"))
	for _, file := range []string{"000001_chat.up.sql", "000002_dispatch.up.sql", "000003_chat_execution.up.sql"} {
		b, e := app.Files.ReadFile(file)
		require.NoError(t, e)
		_, e = p.Exec(ctx, string(b))
		require.NoError(t, e)
	}
	_, err := p.Exec(ctx, "INSERT INTO schema_migrations(version,dirty) VALUES(3,false)")
	require.NoError(t, err)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	id := uuid.NewString()
	require.NoError(t, r.RecordModelAttempt(ctx, chat.ModelAttempt{ID: id, ConversationID: c.ID, TurnID: rc.TurnID, Attempt: 1, Status: "running"}))
	require.NoError(t, r.CompleteModelAttempt(ctx, c.ID, id, "failed", nil, "", "interrupted"))
	require.NoError(t, storage.Migrate(raw, "up"))
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	pending, err := r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.EqualValues(t, 1, pending, "old failed status is not proof of cleanup")
	require.ErrorIs(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "cancelled"}), storage.ErrChatCleanupUnconfirmed)
	// Verify downgrade compatibility and re-upgrade without inventing evidence.
	attachmentDown, err := app.Files.ReadFile("000006_attachments.down.sql")
	require.NoError(t, err)
	_, err = p.Exec(ctx, string(attachmentDown))
	require.NoError(t, err)
	newer, err := app.Files.ReadFile("000005_web_approvals.down.sql")
	require.NoError(t, err)
	_, err = p.Exec(ctx, string(newer))
	require.NoError(t, err)
	down, err := app.Files.ReadFile("000004_chat_cleanup.down.sql")
	require.NoError(t, err)
	_, err = p.Exec(ctx, string(down))
	require.NoError(t, err)
	_, err = p.Exec(ctx, "UPDATE schema_migrations SET version=3")
	require.NoError(t, err)
	require.NoError(t, storage.Migrate(raw, "up"))
	pending, err = r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.EqualValues(t, 1, pending)
}
