//go:build integration

package storage_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContextSummaryChainAndOwnership(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	var seq []int64
	for i := 0; i < 9; i++ {
		in := input(c.ID)
		in.Payload.Text = fmt.Sprintf("question %d", i)
		cmd, e := r.AcceptCommand(ctx, in)
		require.NoError(t, e)
		ref := chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
		require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: fmt.Sprintf("answer %d", i)}))
		require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
		var s int64
		require.NoError(t, p.QueryRow(ctx, "SELECT sequence FROM messages WHERE turn_id=$1 AND result_kind='final'", cmd.TurnID).Scan(&s))
		seq = append(seq, s)
	}
	cmd, e := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, e)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
	require.NoError(t, r.StartChatTurn(ctx, ref))
	inv := uuid.NewString()
	require.NoError(t, r.RegisterChatModelInvocation(ctx, ref, inv, 1))
	attempt := chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: cmd.TurnID, Attempt: 1, Status: "running"}
	_, e = r.BeginChatModelAttempt(ctx, attempt)
	require.NoError(t, e)
	state, e := r.LoadContext(ctx, ref)
	require.NoError(t, e)
	require.Equal(t, 9, state.Total)
	require.Len(t, state.Recent, 6)
	require.Len(t, state.Older, 3)
	require.Equal(t, seq[8], state.Recent[0].Sequence)
	raw, _ := json.Marshal(map[string]any{"previousVersion": 0, "previousBoundary": 0, "exchanges": 3, "sourceLimited": true})
	w := chat.ContextSummaryWrite{TurnRef: ref, AttemptID: attempt.ID, Sequences: seq[:3], Summary: chat.Summary{ID: chat.LogicalID(chat.TurnID(c.ID), "context-summary/1"), ConversationID: c.ID, Version: 1, CoveredSequence: seq[2], Model: c.Model, Text: "canonical memory", Budget: raw}}
	gap := w
	gap.Sequences = []int64{seq[0], seq[2]}
	_, e = r.CommitContextSummary(ctx, gap)
	require.Error(t, e, "skipping eligible completion must reject")
	wrong := w
	wrong.Summary.Model = "gemma4:e4b"
	_, e = r.CommitContextSummary(ctx, wrong)
	require.Error(t, e)
	winner, e := r.CommitContextSummary(ctx, w)
	require.NoError(t, e)
	require.Equal(t, "canonical memory", winner.Text)
	w.Summary.Text = "same owner changed output"
	_, e = r.CommitContextSummary(ctx, w)
	require.Error(t, e, "an owner cannot rewrite its canonical summary")
	w.Summary.Text = "canonical memory"
	winner, e = r.CommitContextSummary(ctx, w)
	require.NoError(t, e)
	require.Equal(t, "canonical memory", winner.Text)
	state, e = r.LoadContext(ctx, ref)
	require.NoError(t, e)
	require.Empty(t, state.Older)
	require.EqualValues(t, seq[2], state.Summary.CoveredSequence)
	require.Equal(t, 19, count(t, p, "messages", c.ID), "full original history remains")
	// A newer logical attempt fences summary publication just like final output.
	attempt.ID = uuid.NewString()
	attempt.Attempt = 2
	_, e = r.BeginChatModelAttempt(ctx, attempt)
	require.NoError(t, e)
	loser := w
	loser.AttemptID = attempt.ID
	loser.Summary.Text = "different attempt candidate"
	winner, e = r.CommitContextSummary(ctx, loser)
	require.NoError(t, e)
	require.Equal(t, "canonical memory", winner.Text)
	w.PreviousVersion = 1
	w.PreviousBoundary = seq[2]
	w.Summary.Version = 2
	w.Summary.ID = chat.LogicalID(chat.TurnID(c.ID), "context-summary/2")
	w.Summary.Budget, _ = json.Marshal(map[string]any{"previousVersion": 1, "previousBoundary": seq[2], "exchanges": 4, "sourceLimited": true})
	w.Summary.CoveredSequence = seq[3]
	w.Sequences = seq[3:4]
	_, e = r.CommitContextSummary(ctx, w)
	require.ErrorIs(t, e, storage.ErrStaleChatAttempt)
	require.Equal(t, 1, count(t, p, "conversation_summaries", c.ID))
	require.NoError(t, r.AcknowledgeChatModelCleanup(ctx, ref, inv))
	w.AttemptID = attempt.ID
	_, e = r.CommitContextSummary(ctx, w)
	require.ErrorIs(t, e, storage.ErrStaleChatAttempt, "no unacknowledged body owns publication")
}
