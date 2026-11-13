//go:build integration

package webintegration_test

import (
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
)

func assertTextOutcome(t *testing.T, f *fixture, conversation chat.ConversationID, turns map[chat.TurnID]textOutcome) {
	t.Helper()
	events, err := f.repo.ListEvents(f.ctx, conversation, 0, 200)
	require.NoError(t, err)
	page, err := f.repo.ListMessages(f.ctx, conversation, "", 200)
	require.NoError(t, err)
	// Never hide a duplicate or unexpected row beyond a page boundary. These
	// text-only fixtures are deliberately small; overflow is a test failure.
	var eventCount, messageCount int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT (SELECT count(*) FROM events WHERE conversation_id=$1),(SELECT count(*) FROM messages WHERE conversation_id=$1)", conversation).Scan(&eventCount, &messageCount))
	require.Equal(t, eventCount, len(events), "event page must cover every retained row")
	require.Equal(t, messageCount, len(page.Messages), "message page must cover every canonical row")
	require.NoError(t, validateTextOutcome(conversation, turns, events, page.Messages))
	for _, e := range events {
		if e.Type != "message.delta" {
			continue
		} // already validated every event domain
		var registered bool
		require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT EXISTS(SELECT 1 FROM model_steps WHERE conversation_id=$1 AND turn_id=$2 AND logical_step=$3 AND attempt=$4)", conversation, e.TurnID, e.LogicalStep, e.Attempt).Scan(&registered))
		require.True(t, registered, "preview must refer to a real registered attempt: %+v", e)
	}
}
