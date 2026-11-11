//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type beforeReceipt struct {
	*storage.Repository
	entered, release chan struct{}
	enter, unblock   sync.Once
	written          chan error
	holdStart        bool
	started          chan struct{}
}

func (r *beforeReceipt) RecordChatAdmission(ctx context.Context, a chat.Admission) error {
	r.enter.Do(func() { close(r.entered) })
	<-r.release
	late, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := r.Repository.RecordChatAdmission(late, a)
	r.written <- err
	return err
}
func (r *beforeReceipt) StartChatTurn(ctx context.Context, ref chat.TurnRef) error {
	if r.holdStart {
		close(r.started)
		<-ctx.Done()
		return ctx.Err()
	}
	return r.Repository.StartChatTurn(ctx, ref)
}
func TestClosedUnreceiptedExecutedCommandNeverBecomesRejected(t *testing.T) {
	for _, hold := range []bool{false, true} {
		name := "completed"
		if hold {
			name = "enqueued_before_start_SQL"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			repo := &beforeReceipt{Repository: f.repo, entered: make(chan struct{}), release: make(chan struct{}), written: make(chan error, 8), holdStart: hold, started: make(chan struct{})}
			f.start(t, repo, mock.New())
			release := func() { repo.unblock.Do(func() { close(repo.release) }) }
			defer release()
			c := f.conversation(t, "qwen3.5:9b")
			cmd := f.command(t, c, "execute before admission SQL")
			submitted := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(f.ctx, time.Second)
				defer cancel()
				_, err := f.gateway.Submit(ctx, cmd)
				submitted <- err
			}()
			select {
			case <-repo.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("receipt barrier not reached")
			}
			if hold {
				select {
				case <-repo.started:
				case <-time.After(10 * time.Second):
					t.Fatal("start barrier not reached")
				}
			} else {
				f.completed(t, cmd)
			}
			require.Error(t, <-submitted)
			receipt, err := f.repo.CheckChatCommand(f.ctx, cmd)
			require.NoError(t, err)
			require.Empty(t, receipt.State)
			require.NoError(t, f.sdk.TerminateWorkflow(f.ctx, c.WorkflowID, "", "close between execution and admission receipt"))
			d := onlyConversation(t, f, c)
			dispatchOne(t, f, d)
			stored, err := f.repo.GetCommand(f.ctx, c.ID, cmd.CommandID)
			require.NoError(t, err)
			assert.Equal(t, "accepted", stored.ResultState, "committed execution is not a pre-execution rejection")
			release()
			select {
			case err := <-repo.written:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("late receipt did not finish")
			}
			receipt, err = f.repo.CheckChatCommand(f.ctx, cmd)
			require.NoError(t, err)
			var dispatched chat.AdmissionResult
			require.NoError(t, json.Unmarshal(stored.Result, &dispatched))
			assert.Equal(t, receipt, dispatched, "late receipt must agree with terminal dispatch result")
			if hold {
				turn, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
				require.NoError(t, err)
				require.Equal(t, "queued", turn.Status, "admission evidence must not be misrepresented as completion")
			}
		})
	}
}
