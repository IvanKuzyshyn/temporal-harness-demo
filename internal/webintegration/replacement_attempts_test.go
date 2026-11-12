//go:build integration

package webintegration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	tw "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Test-only time acceleration. The production workflow and activities run on a
// real server; no cancellation, retry, SQL or workflow decisions are mocked.
type attemptTimeouts struct {
	interceptor.WorkerInterceptorBase
	backoff time.Duration
}
type attemptInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	backoff time.Duration
}
type attemptOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	backoff time.Duration
}

func (i *attemptTimeouts) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &attemptInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, backoff: i.backoff}
}
func (i *attemptInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&attemptOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, backoff: i.backoff})
}
func (i *attemptOutbound) ExecuteActivity(ctx workflow.Context, name string, args ...interface{}) workflow.Future {
	if name == w.WebModelActivity {
		opts := workflow.GetActivityOptions(ctx)
		opts.StartToCloseTimeout = 8 * time.Second
		opts.HeartbeatTimeout = 3 * time.Second
		retry := *opts.RetryPolicy
		retry.InitialInterval = i.backoff
		opts.RetryPolicy = &retry
		ctx = workflow.WithActivityOptions(ctx, opts)
	}
	return i.Next.ExecuteActivity(ctx, name, args...)
}

type oldAttemptModel struct {
	first, timedOut, second, secondCancelled, release, returned, newer chan struct{}
	once                                                               sync.Once
}

func newOldAttemptModel() *oldAttemptModel {
	return &oldAttemptModel{first: make(chan struct{}), timedOut: make(chan struct{}), second: make(chan struct{}), secondCancelled: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}), newer: make(chan struct{}, 4)}
}
func (m *oldAttemptModel) unblock() { m.once.Do(func() { close(m.release) }) }
func (m *oldAttemptModel) Chat(ctx context.Context, r llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if r.Messages[len(r.Messages)-1].Content != "held target" {
		m.newer <- struct{}{}
		return mock.New().Chat(ctx, r, emit)
	}
	if activity.GetInfo(ctx).Attempt == 1 {
		close(m.first)
		<-ctx.Done()
		close(m.timedOut)
		<-m.release // Old remote attempt remains alive after its server timeout.
		close(m.returned)
		return llm.Response{}, ctx.Err()
	}
	close(m.second)
	<-ctx.Done()
	close(m.secondCancelled)
	return llm.Response{}, ctx.Err()
}

func TestReplacementAfterTimedOutAttemptDoesNotBypassCleanup(t *testing.T) {
	for _, backoff := range []bool{false, true} {
		name := "newer_attempt_cancelled"
		delay := 100 * time.Millisecond
		if backoff {
			name = "cancel_during_retry_backoff"
			delay = 10 * time.Second
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			m := newOldAttemptModel()
			a := &activities.ChatActivities{Repository: f.repo, Model: m}
			agents := tw.New(f.sdk, w.WebAgentsQueue, tw.Options{Interceptors: []interceptor.WorkerInterceptor{&attemptTimeouts{backoff: delay}}})
			agents.RegisterWorkflow(w.WebChatSessionWorkflow)
			agents.RegisterActivity(a)
			require.NoError(t, agents.Start())
			stopAgents := sync.OnceFunc(agents.Stop)
			t.Cleanup(stopAgents)
			// Each worker has concurrency one; attempt 1 deliberately holds its slot.
			for n := 0; n < 2; n++ {
				worker := tw.New(f.sdk, w.WebLLMQueue, tw.Options{MaxConcurrentActivityExecutionSize: 1, WorkerStopTimeout: 5 * time.Second, MaxHeartbeatThrottleInterval: 100 * time.Millisecond, DefaultHeartbeatThrottleInterval: 100 * time.Millisecond})
				worker.RegisterActivity(a.ChatModel)
				require.NoError(t, worker.Start())
				t.Cleanup(worker.Stop)
			}
			c := f.conversation(t, "qwen3.5:9b")
			t.Cleanup(func() {
				m.unblock()
				select {
				case <-m.first:
					waitBarrier(t, m.returned)
				default:
				}
			})
			target := f.command(t, c, "held target")
			_, err := f.gateway.Submit(f.ctx, target)
			require.NoError(t, err)
			waitBarrier(t, m.first)
			follow := f.command(t, c, "preserved FIFO")
			_, err = f.gateway.Submit(f.ctx, follow)
			require.NoError(t, err)
			waitBarrier(t, m.timedOut)
			if backoff {
				require.Eventually(t, func() bool {
					d, e := f.sdk.DescribeWorkflowExecution(f.ctx, c.WorkflowID, "")
					return e == nil && len(d.PendingActivities) == 1 && d.PendingActivities[0].LastFailure.GetTimeoutFailureInfo() != nil
				}, 4*time.Second, 20*time.Millisecond, "must cancel AFTER server recorded the first timeout, during retry backoff")
			} else {
				waitBarrier(t, m.second)
			}
			replace := f.control(t, c, target.TurnID, chat.CommandReplace, "replacement must wait")
			admitted, err := f.gateway.Submit(f.ctx, replace)
			require.NoError(t, err)
			require.Equal(t, "accepted", admitted.State)
			if !backoff {
				waitBarrier(t, m.secondCancelled)
			}
			var state w.WebSessionState
			require.Eventually(t, func() bool {
				value, e := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
				return e == nil && value.Get(&state) == nil && (state.Status == "cancelling_interrupted" || state.Cancelled > 0)
			}, 12*time.Second, 50*time.Millisecond)
			require.Equal(t, int64(0), state.Cancelled, "latest logical cancellation does not join the older remote attempt")
			require.Equal(t, "cancelling_interrupted", state.Status)
			// Prove the final logical outcome was cancellation, not the final
			// timeout already covered before I1. A later timeout must not make
			// this regression silently exercise the wrong branch.
			modelEvents := map[int64]bool{}
			logicalCancelled := false
			history := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
			for history.HasNext() {
				event, e := history.Next()
				require.NoError(t, e)
				if a := event.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == w.WebModelActivity {
					modelEvents[event.EventId] = true
				}
				if a := event.GetActivityTaskCanceledEventAttributes(); a != nil && modelEvents[a.ScheduledEventId] {
					logicalCancelled = true
				}
			}
			require.True(t, logicalCancelled, "must cover later cancellation hiding the earlier attempt timeout")
			for _, cmd := range []chat.SessionCommand{target, replace, follow} {
				turn, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
				require.NoError(t, e)
				want := "queued"
				if cmd.TurnID == target.TurnID {
					want = "cancelling"
				}
				require.Equal(t, want, turn.Status)
			}
			select {
			case <-m.newer:
				t.Fatal("replacement/FIFO inference bypassed old cleanup")
			default:
			}
			var cancelled int
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM events WHERE turn_id=$1 AND type='turn.cancelled'", target.TurnID).Scan(&cancelled))
			require.Zero(t, cancelled)
			var pending, acknowledged int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE cleanup_confirmed_at IS NULL),count(*) FILTER(WHERE cleanup_confirmed_at IS NOT NULL) FROM chat_model_invocations WHERE turn_id=$1`, target.TurnID).Scan(&pending, &acknowledged))
			require.Equal(t, 1, pending, "older remote body remains unacknowledged")
			if !backoff {
				require.Equal(t, 1, acknowledged, "newer cancellation acknowledged only its own body")
				// Restart the workflow worker while the OLD provider is still held.
				// Replay must retain the honest parked state, not infer a clean join.
				stopAgents()
				restarted := tw.New(f.sdk, w.WebAgentsQueue, tw.Options{Interceptors: []interceptor.WorkerInterceptor{&attemptTimeouts{backoff: delay}}})
				restarted.RegisterWorkflow(w.WebChatSessionWorkflow)
				restarted.RegisterActivity(a)
				require.NoError(t, restarted.Start())
				t.Cleanup(restarted.Stop)
				value, e := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
				require.NoError(t, e)
				require.NoError(t, value.Get(&state))
				require.Equal(t, "cancelling_interrupted", state.Status)
				require.EqualValues(t, 1, state.UnconfirmedModelInvocations)
				require.Zero(t, state.Cancelled)
			} else {
				require.Zero(t, acknowledged, "retry backoff must not fabricate another invocation")
			}
			// Held work is released ONLY for owned teardown, not as a test success condition.
		})
	}
}
