package activities

import (
	"context"
	"errors"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/temporal"
)

// ChatRepository is the activity seam, not a workflow database dependency.
type ChatRepository interface {
	ResumeChatAdmission(context.Context, chat.AdmissionCheckpoint) (chat.AdmissionResult, error)
	CheckChatCommand(context.Context, chat.SessionCommand) (chat.AdmissionResult, error)
	RecordChatAdmission(context.Context, chat.Admission) error
	StartChatTurn(context.Context, chat.TurnRef) error
	MarkChatCancelling(context.Context, chat.TurnRef) error
	PersistTurnResult(context.Context, chat.TurnResult) error
	GetTurn(context.Context, chat.TurnRef) (chat.Turn, error)
	RecentChatExchanges(context.Context, chat.TurnRef) ([]chat.Exchange, error)
	GetChatModelResult(context.Context, chat.TurnRef) (chat.ChatModelResult, error)
	SaveChatModelResult(context.Context, chat.ChatModelResult) error
	RegisterChatModelInvocation(context.Context, chat.TurnRef, string, int) error
	AcknowledgeChatModelCleanup(context.Context, chat.TurnRef, string) error
	SealChatModelInvocations(context.Context, chat.TurnRef) (int64, error)
	BeginChatModelAttempt(context.Context, chat.ModelAttempt) (bool, error)
	CompleteModelAttempt(context.Context, chat.ConversationID, string, string, []byte, string, string) error
}
type ChatActivities struct {
	Repository  ChatRepository
	Model       llm.Model
	WebTools    *tools.WebRegistry
	Attachments *attachments.Store
}

func chatStorageError(err error) error {
	if errors.Is(err, storage.ErrConflict) || errors.Is(err, chat.ErrInvalid) || errors.Is(err, storage.ErrNotFound) {
		return temporal.NewNonRetryableApplicationError("invalid chat persistence identity", "InvalidRequest", nil)
	}
	return err
}
func (a *ChatActivities) ResumeChatAdmission(ctx context.Context, c chat.AdmissionCheckpoint) (chat.AdmissionResult, error) {
	r, err := a.Repository.ResumeChatAdmission(ctx, c)
	return r, chatStorageError(err)
}
func (a *ChatActivities) CheckChatCommand(ctx context.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
	r, e := a.Repository.CheckChatCommand(ctx, c)
	return r, chatStorageError(e)
}
func (a *ChatActivities) RecordChatAdmission(ctx context.Context, c chat.Admission) error {
	return chatStorageError(a.Repository.RecordChatAdmission(ctx, c))
}
func (a *ChatActivities) SealChatModelInvocations(ctx context.Context, r chat.TurnRef) (int64, error) {
	pending, err := a.Repository.SealChatModelInvocations(ctx, r)
	return pending, chatStorageError(err)
}
func (a *ChatActivities) MarkChatCancelling(ctx context.Context, r chat.TurnRef) error {
	return chatStorageError(a.Repository.MarkChatCancelling(ctx, r))
}
func (a *ChatActivities) StartChatTurn(ctx context.Context, r chat.TurnRef) error {
	return chatStorageError(a.Repository.StartChatTurn(ctx, r))
}
func (a *ChatActivities) PersistTurnResult(ctx context.Context, r chat.TurnResult) error {
	return chatStorageError(a.Repository.PersistTurnResult(ctx, r))
}
