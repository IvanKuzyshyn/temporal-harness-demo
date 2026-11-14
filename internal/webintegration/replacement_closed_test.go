//go:build integration

package webintegration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

type controlReceiptBarrier struct {
	*storage.Repository
	entered, release chan struct{}
	once             sync.Once
	written          chan error
}

func (r *controlReceiptBarrier) RecordChatAdmission(ctx context.Context, a chat.Admission) error {
	if a.Command.Kind != chat.CommandStop {
		return r.Repository.RecordChatAdmission(ctx, a)
	}
	r.once.Do(func() { close(r.entered) })
	<-r.release
	late, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := r.Repository.RecordChatAdmission(late, a)
	r.written <- err
	return err
}
func TestClosedUnreceiptedControlKeepsDecision(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted_stop", true: "stale_stop"}[stale], func(t *testing.T) {
			f := setup(t)
			m := newCancelBarrierModel(t)
			repo := &controlReceiptBarrier{Repository: f.repo, entered: make(chan struct{}), release: make(chan struct{}), written: make(chan error, 8)}
			f.start(t, repo, m)
			t.Cleanup(func() {
				select {
				case <-repo.release:
				default:
					close(repo.release)
				}
				select {
				case <-m.release:
				default:
					close(m.release)
				}
			})
			c := f.conversation(t, "qwen3.5:9b")
			first := f.command(t, c, "target")
			_, err := f.gateway.Submit(f.ctx, first)
			require.NoError(t, err)
			waitBarrier(t, m.started)
			target := first.TurnID
			if stale {
				target = f.command(t, c, "not active").TurnID
			}
			cmd := f.control(t, c, target, chat.CommandStop, "")
			submitted := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(f.ctx, time.Second)
				defer cancel()
				_, err := f.gateway.Submit(ctx, cmd)
				submitted <- err
			}()
			waitBarrier(t, repo.entered)
			require.Error(t, <-submitted)
			require.NoError(t, f.sdk.TerminateWorkflow(f.ctx, c.WorkflowID, "", "test close before control receipt"))
			got, err := f.gateway.Submit(f.ctx, cmd)
			require.NoError(t, err)
			if stale {
				require.Equal(t, "stale_target", got.Code)
			} else {
				require.Equal(t, "accepted", got.State)
				require.Equal(t, first.TurnID, got.TargetTurnID)
			}
			close(repo.release)
			select {
			case err := <-repo.written:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("late receipt blocked")
			}
		})
	}
}
