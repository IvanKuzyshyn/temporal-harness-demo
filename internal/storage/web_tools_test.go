//go:build integration

package storage_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func webAction(t *testing.T, r *storage.Repository) (chat.TurnRef, chat.WebAction) {
	t.Helper()
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	require.NoError(t, r.StartChatTurn(ctx, ref))
	call := chat.WebModelCall{ID: uuid.NewString(), Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com/"}`), ActionHash: strings.Repeat("a", 64), ContentRef: strings.Repeat("b", 64)}
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Calls: []chat.WebModelCall{call}}))
	actions, err := r.PrepareWebActions(ctx, ref)
	require.NoError(t, err)
	require.Len(t, actions.Actions, 1)
	return ref, actions.Actions[0]
}
func TestWebToolScopeCleanupAndAttemptFences(t *testing.T) {
	r, p := database(t)
	ref, a := webAction(t, r)
	a.Outcome = "authorized"
	_, _, err := r.BeginWebTool(ctx, a.Scope, uuid.NewString(), 1)
	require.Error(t, err, "pending is not authorization")
	require.NoError(t, r.RecordWebAuthorization(ctx, a))
	changed := a.Scope
	changed.PolicyRevision++
	_, _, err = r.BeginWebTool(ctx, changed, uuid.NewString(), 1)
	require.Error(t, err)
	old, winner := uuid.NewString(), uuid.NewString()
	args, cached, err := r.BeginWebTool(ctx, a.Scope, old, 1)
	require.NoError(t, err)
	require.False(t, cached)
	require.JSONEq(t, `{"url":"https://example.com/"}`, string(args))
	_, _, err = r.BeginWebTool(ctx, a.Scope, winner, 2)
	require.NoError(t, err)
	require.ErrorIs(t, r.SaveWebToolResult(ctx, a.Scope, old, chat.WebToolResult{Text: "stale"}), storage.ErrStaleChatAttempt)
	require.NoError(t, r.SaveWebToolResult(ctx, a.Scope, winner, chat.WebToolResult{Text: "canonical"}))
	require.NoError(t, r.AcknowledgeWebToolCleanup(ctx, a.Scope, winner))
	require.NoError(t, r.MarkChatCancelling(ctx, ref))
	count, err := r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	for _, status := range []string{"cancelled", "completed", "failed"} {
		require.ErrorIs(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: status}), storage.ErrChatCleanupUnconfirmed)
	}
	require.NoError(t, r.AcknowledgeWebToolCleanup(ctx, a.Scope, old))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "cancelled"}))
	_, _, err = r.BeginWebTool(ctx, a.Scope, uuid.NewString(), 3)
	require.ErrorIs(t, err, storage.ErrStaleChatAttempt)
	var n int
	require.NoError(t, p.QueryRow(ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1`, ref.TurnID).Scan(&n))
	require.Equal(t, 2, n)
}
func TestWebToolResultImmutableForItsWinningOwner(t *testing.T) {
	r, _ := database(t)
	_, a := webAction(t, r)
	a.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, a))
	id := uuid.NewString()
	_, _, err := r.BeginWebTool(ctx, a.Scope, id, 1)
	require.NoError(t, err)
	require.NoError(t, r.SaveWebToolResult(ctx, a.Scope, id, chat.WebToolResult{Text: "first"}))
	require.NoError(t, r.SaveWebToolResult(ctx, a.Scope, id, chat.WebToolResult{Text: "first"}))
	require.ErrorIs(t, r.SaveWebToolResult(ctx, a.Scope, id, chat.WebToolResult{Text: "changed"}), storage.ErrConflict)
}
func TestWebToolFetchMetadataBoundAndImmutable(t *testing.T) {
	r, _ := database(t)
	_, a := webAction(t, r)
	a.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, a))
	id := uuid.NewString()
	_, _, err := r.BeginWebTool(ctx, a.Scope, id, 1)
	require.NoError(t, err)
	for _, field := range []string{"requested", "final", "type"} {
		bad := chat.WebToolResult{Text: "content"}
		switch field {
		case "requested":
			bad.RequestedURL = strings.Repeat("u", 2049)
		case "final":
			bad.FinalURL = strings.Repeat("u", 2049)
		case "type":
			bad.ContentType = strings.Repeat("t", 129)
		}
		require.ErrorIs(t, r.SaveWebToolResult(ctx, a.Scope, id, bad), chat.ErrInvalid, field)
	}
	result := chat.WebToolResult{Text: "content", RequestedURL: "https://example.com/", FinalURL: "https://example.com/final", ContentType: "text/html", Truncated: true}
	require.NoError(t, r.SaveWebToolResult(ctx, a.Scope, id, result))
	got, err := r.WebToolResult(ctx, a.Scope.ConversationID, a.Scope.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, result, got)
	changed := result
	changed.FinalURL = "https://other.example/"
	require.ErrorIs(t, r.SaveWebToolResult(ctx, a.Scope, id, changed), storage.ErrConflict)
	changed = result
	changed.Truncated = false
	require.ErrorIs(t, r.SaveWebToolResult(ctx, a.Scope, id, changed), storage.ErrConflict)
}

func TestWebToolOrdinaryCompletionRetainsMissingAck(t *testing.T) {
	r, p := database(t)
	ref, a := webAction(t, r)
	a.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, a))
	_, _, err := r.BeginWebTool(ctx, a.Scope, uuid.NewString(), 1)
	require.NoError(t, err)
	ref.Step = 1
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "canonical failed-tool explanation"}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	var n int
	require.NoError(t, p.QueryRow(ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL`, ref.TurnID).Scan(&n))
	require.Equal(t, 1, n)
}
