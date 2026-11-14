//go:build integration

package storage_test

import (
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChatOverlappingAttemptsFenceLatePublisher(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	one := chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: rc.TurnID, Attempt: 1, Status: "running"}
	two := one
	two.ID = uuid.NewString()
	two.Attempt = 2
	_, err = r.GetChatModelResult(ctx, ref)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.False(t, beginChatAttempt(t, r, one))
	// Both cache reads precede publication, with an explicit barrier holding the
	// old remote attempt until the new attempt has registered (not a restart).
	read := make(chan struct{})
	release := make(chan struct{})
	late := make(chan error, 1)
	go func() {
		close(read)
		<-release
		late <- r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "same answer", AttemptID: one.ID})
	}()
	<-read
	_, err = r.GetChatModelResult(ctx, ref)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.False(t, beginChatAttempt(t, r, two))
	close(release)
	require.ErrorIs(t, <-late, storage.ErrStaleChatAttempt, "old attempt must not publish after its ownership was superseded")
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "same answer", AttemptID: two.ID}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	var status, id string
	require.NoError(t, p.QueryRow(ctx, "SELECT status,coalesce(result_entry_id::text,'') FROM model_steps WHERE id=$1", two.ID).Scan(&status, &id))
	require.Equal(t, "completed", status)
	require.Equal(t, chat.LogicalID(rc.TurnID, "final"), id)
}
func TestChatCandidateCommittedBeforeRegistrationRetainsWinningAttempt(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	one := chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: rc.TurnID, Attempt: 1, Status: "running"}
	two := one
	two.ID = uuid.NewString()
	two.Attempt = 2
	require.False(t, beginChatAttempt(t, r, one))
	_, err = r.GetChatModelResult(ctx, ref)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "winner", AttemptID: one.ID}))
	require.True(t, beginChatAttempt(t, r, two))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	var status, id string
	require.NoError(t, p.QueryRow(ctx, "SELECT status,coalesce(result_entry_id::text,'') FROM model_steps WHERE id=$1", one.ID).Scan(&status, &id))
	require.Equal(t, "completed", status)
	require.Equal(t, chat.LogicalID(rc.TurnID, "final"), id)
}
