//go:build integration

package storage_test

import (
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChatCleanupDoesNotInferAcknowledgmentFromPublisherStatus(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	old, newer := uuid.NewString(), uuid.NewString()
	for n, id := range []string{old, newer} {
		require.NoError(t, r.RegisterChatModelInvocation(ctx, ref, id, n+1))
		require.False(t, beginChatAttempt(t, r, chat.ModelAttempt{ID: id, ConversationID: c.ID, TurnID: rc.TurnID, Attempt: n + 1, Status: "running"}))
	}
	var status, reason string
	require.NoError(t, p.QueryRow(ctx, "SELECT status,error FROM model_steps WHERE id=$1", old).Scan(&status, &reason))
	require.Equal(t, "failed", status)
	require.Equal(t, "interrupted", reason)
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "latest candidate", AttemptID: newer}))
	require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, newer))
	pending, err := r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.EqualValues(t, 1, pending)
	for _, status := range []string{"completed", "failed", "cancelled"} {
		require.ErrorIs(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: status}), storage.ErrChatCleanupUnconfirmed)
	}
	var events int
	require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM events WHERE turn_id=$1", rc.TurnID).Scan(&events))
	require.Zero(t, events)
	require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, old))
	require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, old), "ack retry is idempotent")
	pending, err = r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.Zero(t, pending)
	require.ErrorIs(t, r.RegisterChatModelInvocation(ctx, ref, uuid.NewString(), 3), storage.ErrStaleChatAttempt)
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "cancelled", Reason: "replaced"}))
}

func TestChatCleanupTracksDuplicateInvocationAndBindsAcknowledgment(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	other, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	one, two := uuid.NewString(), uuid.NewString()
	require.NoError(t, r.RegisterChatModelInvocation(ctx, ref, one, 1))
	require.NoError(t, r.RegisterChatModelInvocation(ctx, ref, two, 1), "same Temporal attempt, distinct bodies")
	require.ErrorIs(t, r.AcknowledgeChatModelCleanup(ctx, chat.TurnRef{ConversationID: c.ID, TurnID: other.TurnID}, one), storage.ErrConflict)
	require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, one))
	require.Error(t, r.RegisterChatModelInvocation(ctx, ref, one, 1), "an acknowledged ID cannot authorize another body")
	pending, err := r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.EqualValues(t, 1, pending)
	require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, two))
	pending, err = r.SealChatModelInvocations(ctx, ref)
	require.NoError(t, err)
	require.Zero(t, pending)
}

func TestChatCleanupSealSerializesRegistration(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	for n := 0; n < 20; n++ {
		rc, err := r.AcceptCommand(ctx, input(c.ID))
		require.NoError(t, err)
		ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
		id := uuid.NewString()
		start := make(chan struct{})
		registered := make(chan error, 1)
		go func() { <-start; registered <- r.RegisterChatModelInvocation(ctx, ref, id, 1) }()
		close(start)
		pending, sealErr := r.SealChatModelInvocations(ctx, ref)
		registrationErr := <-registered
		require.NoError(t, sealErr)
		if registrationErr == nil {
			require.EqualValues(t, 1, pending, "committed registration must be visible before the seal returns")
			require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, id))
		} else {
			require.ErrorIs(t, registrationErr, storage.ErrStaleChatAttempt)
			require.Zero(t, pending, "seal winning admission must reject the body")
		}
		require.ErrorIs(t, r.RegisterChatModelInvocation(ctx, ref, uuid.NewString(), 2), storage.ErrStaleChatAttempt)
	}
}
