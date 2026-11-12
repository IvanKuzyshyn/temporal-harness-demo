//go:build integration

package storage_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	modelcontext "github.com/IvanKuzyshyn/temporal-harness-demo/internal/context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContextEvidenceAuthorizationOwnershipAndOrdering(t *testing.T) {
	r, p := database(t)
	ref, a := webAction(t, r)
	a.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, a))
	owner := uuid.NewString()
	_, _, e := r.BeginWebTool(ctx, a.Scope, owner, 1)
	require.NoError(t, e)
	original := chat.WebToolResult{Text: "Recorded source observation", RequestedURL: "https://example.com/", FinalURL: "https://example.com/final", Truncated: true}
	require.NoError(t, r.SaveWebToolResult(ctx, a.Scope, owner, original))
	require.NoError(t, r.AcknowledgeWebToolCleanup(ctx, a.Scope, owner))
	complete := func(ref chat.TurnRef) {
		ref.Step++
		require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "contradictory interpretation"}))
		require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	}
	complete(ref)
	c, e := r.GetConversation(ctx, ref.ConversationID)
	require.NoError(t, e)
	var failedID string
	for _, authorized := range []bool{true, false} {
		cmd, e := r.AcceptCommand(ctx, input(c.ID))
		require.NoError(t, e)
		other := chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
		require.NoError(t, r.StartChatTurn(ctx, other))
		call := chat.WebModelCall{ID: uuid.NewString(), Name: "web_fetch", Arguments: []byte(`{"url":"https://example.com/other"}`), ActionHash: strings.Repeat("c", 64), ContentRef: strings.Repeat("d", 64)}
		require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: other, Calls: []chat.WebModelCall{call}}))
		actions, e := r.PrepareWebActions(ctx, other)
		require.NoError(t, e)
		action := actions.Actions[0]
		if authorized {
			action.Outcome = "authorized"
			require.NoError(t, r.RecordWebAuthorization(ctx, action))
			inv := uuid.NewString()
			_, _, e = r.BeginWebTool(ctx, action.Scope, inv, 1)
			require.NoError(t, e)
			require.NoError(t, r.SaveWebToolResult(ctx, action.Scope, inv, chat.WebToolResult{Error: "source unavailable"}))
			require.NoError(t, r.AcknowledgeWebToolCleanup(ctx, action.Scope, inv))
			failedID = call.ID
		} else {
			action.Outcome = "denied"
			require.NoError(t, r.RecordWebAuthorization(ctx, action))
		}
		complete(other)
	}
	// An unrelated authorized conversation is not context for this one.
	cross, crossAction := webAction(t, r)
	crossAction.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, crossAction))
	inv := uuid.NewString()
	_, _, e = r.BeginWebTool(ctx, crossAction.Scope, inv, 1)
	require.NoError(t, e)
	require.NoError(t, r.SaveWebToolResult(ctx, crossAction.Scope, inv, chat.WebToolResult{Text: "CROSS CONVERSATION SECRET"}))
	require.NoError(t, r.AcknowledgeWebToolCleanup(ctx, crossAction.Scope, inv))
	complete(cross)
	for i := 0; i < 7; i++ {
		cmd, e := r.AcceptCommand(ctx, input(c.ID))
		require.NoError(t, e)
		complete(chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
	}
	cmd, e := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, e)
	current := chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
	state, e := r.LoadContext(ctx, current)
	require.NoError(t, e)
	require.Equal(t, 2, state.EvidenceTotal)
	require.Len(t, state.Evidence, 2)
	require.Equal(t, failedID, state.Evidence[0].Source.Scope.ToolCallID)
	require.Equal(t, "source unavailable", state.Evidence[0].Result.Error)
	evidence := state.Evidence[1]
	require.Equal(t, a.Scope, evidence.Source.Scope)
	require.Equal(t, original, evidence.Result)
	require.True(t, chat.ValidHash(evidence.Source.ArgumentsHash))
	require.True(t, chat.ValidHash(evidence.Source.ResultHash))
	require.JSONEq(t, `{"url":"https://example.com/"}`, string(evidence.Arguments))
	for _, exchange := range state.Recent {
		require.Empty(t, exchange.Results, "retained evidence is not duplicated in recent exchanges")
	}
	raw, _ := json.Marshal(state)
	require.NotContains(t, string(raw), "CROSS CONVERSATION SECRET")
	require.ErrorIs(t, r.SaveWebToolResult(ctx, a.Scope, owner, chat.WebToolResult{Text: "rewritten"}), storage.ErrConflict)
	// Fresh repository object against durable SQL has exactly the same observation.
	again, e := storage.New(p).LoadContext(ctx, current)
	require.NoError(t, e)
	require.Equal(t, state.Evidence, again.Evidence)
	var latest string
	for i := 0; i < 5; i++ {
		cmd, e := r.AcceptCommand(ctx, input(c.ID))
		require.NoError(t, e)
		ref := chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
		require.NoError(t, r.StartChatTurn(ctx, ref))
		call := chat.WebModelCall{ID: uuid.NewString(), Name: "web_fetch", Arguments: []byte(`{"url":"https://example.com/newer"}`), ActionHash: strings.Repeat("e", 64), ContentRef: strings.Repeat("f", 64)}
		require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Calls: []chat.WebModelCall{call}}))
		actions, e := r.PrepareWebActions(ctx, ref)
		require.NoError(t, e)
		action := actions.Actions[0]
		action.Outcome = "authorized"
		require.NoError(t, r.RecordWebAuthorization(ctx, action))
		inv := uuid.NewString()
		_, _, e = r.BeginWebTool(ctx, action.Scope, inv, 1)
		require.NoError(t, e)
		require.NoError(t, r.SaveWebToolResult(ctx, action.Scope, inv, chat.WebToolResult{Text: "identical text does not collapse distinct actions"}))
		require.NoError(t, r.AcknowledgeWebToolCleanup(ctx, action.Scope, inv))
		complete(ref)
		latest = call.ID
	}
	bounded, e := r.LoadContext(ctx, current)
	require.NoError(t, e)
	require.Equal(t, 7, bounded.EvidenceTotal)
	require.Len(t, bounded.Evidence, chat.MaxContextEvidence)
	require.Equal(t, latest, bounded.Evidence[0].Source.Scope.ToolCallID)
	for i := 1; i < len(bounded.Evidence); i++ {
		require.Greater(t, bounded.Evidence[i-1].Source.Sequence, bounded.Evidence[i].Source.Sequence)
	}
	for _, exchange := range bounded.Recent {
		require.Empty(t, exchange.Results, "recent evidence is supplied only by the bounded independent selection")
	}
	mandatory, e := modelcontext.Mandatory(chat.Turn{Model: c.Model, Text: "Recall without new analysis"}, "Limited memory.")
	require.NoError(t, e)
	builder := modelcontext.Builder{Repository: r, Mandatory: mandatory}
	assembled, e := builder.Build(ctx, c.ID, current.TurnID, modelcontext.DefaultBudget())
	require.NoError(t, e)
	require.Len(t, assembled.Coverage.Evidence.Included, 4)
	require.Equal(t, 3, assembled.Coverage.Evidence.Omitted)
	require.LessOrEqual(t, assembled.Accounting.Total, 16384)
	require.Len(t, assembled.Coverage.Quotations.Included, 4)
	metadata, e := json.Marshal(chat.ContextMetadata{Coverage: assembled.Coverage, Accounting: assembled.Accounting})
	require.NoError(t, e)
	require.LessOrEqual(t, len(metadata), 8192, "combined maximum source descriptors fit existing SQL metadata cap")
	require.NotContains(t, string(metadata), "identical text does not collapse")
}
