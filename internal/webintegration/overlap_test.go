//go:build integration

package webintegration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	tc "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	tw "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Test workflow changes only the timeout to force a real remote activity retry.
// Production ChatModel, storage activities and transactions remain unchanged.
func overlapWorkflow(ctx workflow.Context, ref chat.TurnRef) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: w.WebAgentsQueue, StartToCloseTimeout: 10 * time.Second})
	if err := workflow.ExecuteActivity(ctx, w.WebStartTurnActivity, ref).Get(ctx, nil); err != nil {
		return err
	}
	model := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: w.WebLLMQueue, StartToCloseTimeout: 2 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: 100 * time.Millisecond}})
	if err := workflow.ExecuteActivity(model, w.WebModelActivity, ref).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.ExecuteActivity(ctx, w.WebPersistTurnActivity, chat.TurnResult{TurnRef: ref, Status: "completed"}).Get(ctx, nil)
}

type overlapRepository struct {
	*storage.Repository
	publishBeforeRegister bool
	releaseOnce           sync.Once
	release               chan struct{}
	published             chan error
	misses                atomic.Int32
}

func (r *overlapRepository) unblock() { r.releaseOnce.Do(func() { close(r.release) }) }
func (r *overlapRepository) GetChatModelResult(ctx context.Context, ref chat.TurnRef) (chat.ChatModelResult, error) {
	v, err := r.Repository.GetChatModelResult(ctx, ref)
	if errors.Is(err, storage.ErrNotFound) {
		r.misses.Add(1)
	}
	return v, err
}
func (r *overlapRepository) SaveChatModelResult(ctx context.Context, v chat.ChatModelResult) error {
	if v.AttemptID == chat.LogicalID(v.TurnID, "model/0/attempt/1") {
		<-r.release
		// Simulate an already issued SQL write surviving remote timeout/cancellation.
		late, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := r.Repository.SaveChatModelResult(late, v)
		r.published <- err
		return err
	}
	return r.Repository.SaveChatModelResult(ctx, v)
}
func (r *overlapRepository) BeginChatModelAttempt(ctx context.Context, a chat.ModelAttempt) (bool, error) {
	if a.Attempt != 2 {
		return r.Repository.BeginChatModelAttempt(ctx, a)
	}
	if r.publishBeforeRegister {
		r.unblock()
		if err := <-r.published; err != nil {
			return false, err
		}
		return r.Repository.BeginChatModelAttempt(ctx, a)
	}
	cached, err := r.Repository.BeginChatModelAttempt(ctx, a)
	r.unblock()
	if lateErr := <-r.published; !errors.Is(lateErr, storage.ErrStaleChatAttempt) {
		return false, errors.New("superseded attempt was not fenced")
	}
	return cached, err
}
func TestOverlappingChatModelAttemptsRealTemporal(t *testing.T) {
	for _, before := range []bool{false, true} {
		name := "register_then_late_publish"
		if before {
			name = "publish_between_cache_read_and_register"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			repo := &overlapRepository{Repository: f.repo, publishBeforeRegister: before, release: make(chan struct{}), published: make(chan error, 1)}
			f.start(t, repo, mock.New())
			f.start(t, repo, mock.New())
			t.Cleanup(repo.unblock)
			c := f.conversation(t, "qwen3.5:9b")
			cmd := f.command(t, c, "same answer across attempts")
			runner := tw.New(f.sdk, "web-chat-overlap-test", tw.Options{})
			runner.RegisterWorkflow(overlapWorkflow)
			require.NoError(t, runner.Start())
			defer runner.Stop()
			run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: "web-chat-overlap-test"}, overlapWorkflow, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
			require.NoError(t, err)
			wait, cancel := context.WithTimeout(f.ctx, 20*time.Second)
			defer cancel()
			require.NoError(t, run.Get(wait, nil))
			require.GreaterOrEqual(t, repo.misses.Load(), int32(2))
			winner := 2
			if before {
				winner = 1
			}
			var status, entry string
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT status,coalesce(result_entry_id::text,'') FROM model_steps WHERE turn_id=$1 AND attempt=$2", cmd.TurnID, winner).Scan(&status, &entry))
			require.Equal(t, "completed", status)
			require.Equal(t, chat.LogicalID(cmd.TurnID, "final"), entry)
			var running, answers int
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM model_steps WHERE turn_id=$1 AND status='running'", cmd.TurnID).Scan(&running))
			require.Zero(t, running)
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM messages WHERE turn_id=$1 AND role='assistant'", cmd.TurnID).Scan(&answers))
			require.Equal(t, 1, answers)
			var winningAttempt string
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT attempt_id::text FROM chat_model_results WHERE conversation_id=$1 AND turn_id=$2 AND logical_step=0", c.ID, cmd.TurnID).Scan(&winningAttempt))
			require.Equal(t, chat.LogicalID(cmd.TurnID, fmt.Sprintf("model/0/attempt/%d", winner)), winningAttempt)
			assertTextOutcome(t, f, c.ID, map[chat.TurnID]textOutcome{cmd.TurnID: {completed: true, maxAttempt: 2}})
			t.Logf("real activity timeout/overlap: winner attempt %d, one final answer/canonical completion, registered bounded previews, no running attempts", winner)
		})
	}
}
