//go:build integration

package storage_test

import (
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
	"testing"
)

// Missing/partial final projection must never be observable as completion.
func TestWebChatCompletedOnlyAfterPersistence(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "answer", Tokens: 2}))
	result := chat.TurnResult{TurnRef: ref, Status: "completed"}
	// Test-owned temporary fault: transaction must roll back message/status too.
	_, err = p.Exec(ctx, `CREATE FUNCTION fail_chat_event_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test outage'; END $$; CREATE TRIGGER fail_chat_event_test BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION fail_chat_event_test()`)
	require.NoError(t, err)
	require.Error(t, r.PersistTurnResult(ctx, result))
	turn, err := r.GetTurn(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "queued", turn.Status)
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	_, err = p.Exec(ctx, `DROP TRIGGER fail_chat_event_test ON events; DROP FUNCTION fail_chat_event_test()`)
	require.NoError(t, err)
	require.NoError(t, r.PersistTurnResult(ctx, result))
	require.NoError(t, r.PersistTurnResult(ctx, result))
	turn, err = r.GetTurn(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "completed", turn.Status)
	page, err = r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	require.Equal(t, "answer", page.Messages[1].Text)
	events, err := r.ListEvents(ctx, c.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "turn.completed", events[0].Type)
	result.Status = "failed"
	result.Reason = "model_error"
	require.ErrorIs(t, r.PersistTurnResult(ctx, result), storage.ErrConflict)
}
func TestModelAttemptRetryOneLogicalAnswer(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	candidate := chat.ChatModelResult{TurnRef: ref, Text: "stable", Tokens: 3}
	require.NoError(t, r.SaveChatModelResult(ctx, candidate))
	require.NoError(t, r.SaveChatModelResult(ctx, candidate))
	candidate.Text = "changed"
	require.ErrorIs(t, r.SaveChatModelResult(ctx, candidate), storage.ErrConflict)
	got, err := r.GetChatModelResult(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "stable", got.Text)
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
}
func TestMissingDisplayEventsDoNotLoseHistory(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "durable without deltas"}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	events, err := r.ListEvents(ctx, c.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 1)
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Equal(t, "durable without deltas", page.Messages[1].Text)
}
func TestChatAdmissionIdentityAndPermanentReceipt(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	in := input(c.ID)
	rc, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	cmd := chat.SessionCommand{ConversationID: c.ID, CommandID: rc.CommandID, TurnID: rc.TurnID, RequestID: in.RequestID, PayloadDigest: rc.PayloadDigest, Kind: in.Kind, Payload: in.Payload, WorkflowID: c.WorkflowID, Model: c.Model, Sequence: 1}
	receipt, err := r.CheckChatCommand(ctx, cmd)
	require.NoError(t, err)
	require.Empty(t, receipt.State)
	require.NoError(t, r.RecordChatAdmission(ctx, chat.Admission{Command: cmd, Result: chat.AdmissionResult{State: "accepted"}}))
	receipt, err = r.CheckChatCommand(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, "accepted", receipt.State)
	cmd.RequestID = "00000000-0000-4000-8000-000000000001"
	_, err = r.CheckChatCommand(ctx, cmd)
	require.ErrorIs(t, err, storage.ErrConflict)
	var raw json.RawMessage
	require.NoError(t, p.QueryRow(ctx, "SELECT execution_receipt FROM commands WHERE id=$1", rc.CommandID).Scan(&raw))
	require.JSONEq(t, `{"state":"accepted"}`, string(raw))
}
