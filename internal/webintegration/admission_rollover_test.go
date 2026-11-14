//go:build integration

package webintegration_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
)

type admissionOutage struct {
	*storage.Repository
	pool  *pgxpool.Pool
	stage string
	down  atomic.Bool
}

func (r *admissionOutage) failure(ctx context.Context, stage string) error {
	if stage == r.stage && r.down.Load() {
		_, err := r.pool.Exec(ctx, "SELECT 1/0")
		return err
	}
	return nil
}
func (r *admissionOutage) CheckChatCommand(ctx context.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
	if err := r.failure(ctx, "check"); err != nil {
		return chat.AdmissionResult{}, err
	}
	return r.Repository.CheckChatCommand(ctx, c)
}
func (r *admissionOutage) RecordChatAdmission(ctx context.Context, a chat.Admission) error {
	if err := r.failure(ctx, "receipt"); err != nil {
		return err
	}
	return r.Repository.RecordChatAdmission(ctx, a)
}
func (r *admissionOutage) ResumeChatAdmission(ctx context.Context, c chat.AdmissionCheckpoint) (chat.AdmissionResult, error) {
	if err := r.failure(ctx, c.Phase); err != nil {
		return chat.AdmissionResult{}, err
	}
	return r.Repository.ResumeChatAdmission(ctx, c)
}
func TestAdmissionOutageRollsOverBeforeRestoration(t *testing.T) {
	for _, stage := range []string{"check", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t)
			repo := &admissionOutage{Repository: f.repo, pool: f.pool, stage: stage}
			repo.down.Store(true)
			f.start(t, repo, mock.New())
			c := f.conversation(t, "qwen3.5:9b")
			run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: w.WebAgentsQueue}, w.WebSessionName, w.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 1})
			require.NoError(t, err)
			cmd := f.command(t, c, "Admission checkpoint MUST NOT carry this text.")
			submit, cancel := context.WithTimeout(f.ctx, time.Second)
			_, err = f.gateway.Submit(submit, cmd)
			cancel()
			require.Error(t, err)
			id := run.GetRunID()
			// Remain unavailable through TWO run boundaries. Neither handler completion
			// nor CAN is allowed to depend on storage restoration.
			for boundary := 0; boundary < 2; boundary++ {
				next := ""
				require.Eventually(t, func() bool {
					it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, id, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
					scheduled := 0
					for it.HasNext() {
						e, err := it.Next()
						if err != nil {
							return false
						}
						if e.GetActivityTaskScheduledEventAttributes() != nil {
							scheduled++
						}
						if a := e.GetWorkflowExecutionContinuedAsNewEventAttributes(); a != nil {
							next = a.NewExecutionRunId
							require.NotContains(t, string(a.Input.Payloads[0].Data), cmd.Payload.Text)
						}
					}
					if next != "" {
						require.LessOrEqual(t, scheduled, 2)
					}
					return next != ""
				}, 30*time.Second, 100*time.Millisecond)
				id = next
			}
			repo.down.Store(false)
			d := onlyConversation(t, f, c)
			require.Eventually(t, func() bool {
				_, err := d.RunOnce(f.ctx)
				if err != nil {
					return false
				}
				stored, err := f.repo.GetCommand(f.ctx, c.ID, cmd.CommandID)
				return err == nil && stored.ResultState == "accepted"
			}, 30*time.Second, 100*time.Millisecond)
			f.completed(t, cmd)
			_, err = f.gateway.Submit(f.ctx, cmd)
			require.NoError(t, err)
			page, err := f.repo.ListMessages(f.ctx, c.ID, "", 10)
			require.NoError(t, err)
			require.Len(t, page.Messages, 2)
			events, err := f.repo.ListEvents(f.ctx, c.ID, 0, 10)
			require.NoError(t, err)
			require.Len(t, events, 1)
			t.Logf("real %s outage survived two bounded CAN handoffs before restore; same dispatcher identity recovered once", stage)
		})
	}
}
