package activities

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type attachmentModelRepository struct{ budgetWebRepository }

func (r *attachmentModelRepository) WebAttachmentChecksum(context.Context, chat.ConversationID, chat.AttachmentID) (string, error) {
	return strings.Repeat("a", 64), nil
}
func (r *attachmentModelRepository) WebToolResult(context.Context, chat.ConversationID, string) (chat.WebToolResult, error) {
	return chat.WebToolResult{Error: "denied"}, nil
}
func TestAttachmentCleanupUncertaintyCannotAcknowledge(t *testing.T) {
	for _, failure := range []error{attachments.ErrCleanupUnconfirmed, attachments.ErrPersistence} {
		s, args := fetchScope(t, "http://example.com/")
		repo := &fetchActivityRepository{scope: s, args: args, authorized: true}
		a := ChatActivities{Repository: repo, WebTools: tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": func(context.Context, chat.ApprovalScope, json.RawMessage) (chat.WebToolResult, error) {
			return chat.WebToolResult{}, failure
		}})}
		e := a.ExecuteWebTool(context.Background(), chat.WebToolExecution{Scope: s})
		require.ErrorIs(t, e, failure)
		require.Empty(t, repo.result, "no settled result on infrastructure failure")
		require.Equal(t, !errors.Is(failure, attachments.ErrCleanupUnconfirmed), repo.acked)
	}
}

func TestUnapprovedFileNeverEntersModel(t *testing.T) {
	ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
	id := chat.AttachmentID(uuid.NewString())
	repo := &attachmentModelRepository{budgetWebRepository: budgetWebRepository{turn: chat.Turn{Model: "qwen3.5:9b", Text: "analyse attachment", AttachmentIDs: []chat.AttachmentID{id}}}}
	model := &budgetWebModel{}
	a := ChatActivities{Repository: repo, Model: model}
	_, e := a.ChatStep(context.Background(), ref)
	require.NoError(t, e)
	require.Nil(t, model.request, "new attachments require deterministic approval before any inference")
	require.NotNil(t, repo.saved)
	require.Len(t, repo.saved.Calls, 1)
	require.Equal(t, "file_upload", repo.saved.Calls[0].Name)
	require.Contains(t, string(repo.saved.Calls[0].Arguments), string(id))
	require.NotContains(t, string(repo.saved.Calls[0].Arguments), "SECRET ORIGINAL CONTENT")
	ref.Step = 1
	_, e = a.ChatStep(context.Background(), ref)
	require.NoError(t, e)
	require.NotNil(t, model.request)
	raw, e := json.Marshal(model.request)
	require.NoError(t, e)
	require.NotContains(t, string(raw), "SECRET ORIGINAL CONTENT")
	require.Contains(t, string(raw), "denied")
}
