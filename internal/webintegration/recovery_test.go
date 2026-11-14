//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
)

func onlyConversation(t *testing.T, f *fixture, c chat.Conversation) *dispatch.Dispatcher {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE conversation_id<>$1", c.ID)
	require.NoError(t, err)
	d, err := dispatch.New(f.repo, f.gateway, dispatch.Options{RetryBase: 50 * time.Millisecond, RetryMax: time.Second})
	require.NoError(t, err)
	return d
}
func dispatchOne(t *testing.T, f *fixture, d *dispatch.Dispatcher) {
	t.Helper()
	worked, err := d.RunOnce(f.ctx)
	require.NoError(t, err)
	require.True(t, worked)
}

func TestPersistenceExhaustionRecoversAdmittedInboxAndDispatcher(t *testing.T) {
	f := setup(t)
	repo := &settlementGate{Repository: f.repo, entered: make(chan struct{}), release: make(chan struct{})}
	f.start(t, repo, mock.New())
	c := f.conversation(t, "qwen3.5:9b")
	run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 3})
	require.NoError(t, err)
	a := f.command(t, c, "first")
	b := f.command(t, c, "second already admitted")
	d := onlyConversation(t, f, c)
	dispatchOne(t, f, d)
	dispatchOne(t, f, d)
	select {
	case <-repo.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("settlement not reached")
	}
	// Real SQL failure rolls the final transaction back on every bounded attempt.
	_, err = f.pool.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION fail_exhaustion_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.conversation_id='%s'::uuid THEN RAISE EXCEPTION 'test persistence unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_exhaustion_test BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION fail_exhaustion_test()`, c.ID))
	require.NoError(t, err)
	restore := func() {
		_, err := f.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS fail_exhaustion_test ON events; DROP FUNCTION IF EXISTS fail_exhaustion_test()`)
		require.NoError(t, err)
	}
	defer restore()
	close(repo.release)
	// Prove the actual Temporal activity retry batch exhausted, not just one error.
	require.Eventually(t, func() bool {
		it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for it.HasNext() {
			e, err := it.Next()
			if err != nil {
				return false
			}
			if a := e.GetActivityTaskFailedEventAttributes(); a != nil && a.RetryState == enumspb.RETRY_STATE_MAXIMUM_ATTEMPTS_REACHED {
				return true
			}
		}
		return false
	}, 30*time.Second, 100*time.Millisecond)
	v, err := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
	require.NoError(t, err)
	var state workflows.WebSessionState
	require.NoError(t, v.Get(&state))
	assert.Equal(t, "recovering", state.Status)
	assert.Equal(t, a.TurnID, state.ActiveTurn)
	require.Len(t, state.Inbox, 1)
	require.Equal(t, b.TurnID, state.Inbox[0].TurnID)
	assertTextOutcome(t, f, c.ID, map[chat.TurnID]textOutcome{a.TurnID: {maxAttempt: 1}, b.TurnID: {maxAttempt: 1}})
	restore()
	third := f.command(t, c, "third through real dispatcher after restore")
	require.Eventually(t, func() bool {
		_, err := d.RunOnce(f.ctx)
		if err != nil {
			return false
		}
		r, err := f.repo.GetCommand(f.ctx, c.ID, third.CommandID)
		return err == nil && r.ResultState == "accepted"
	}, 20*time.Second, 100*time.Millisecond, "third must not remain an endless pending FIFO head")
	f.completed(t, a)
	f.completed(t, b)
	f.completed(t, third)
	page, err := f.repo.ListMessages(f.ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 6)
	assertTextOutcome(t, f, c.ID, map[chat.TurnID]textOutcome{
		a.TurnID:     {completed: true, maxAttempt: 1},
		b.TurnID:     {completed: true, maxAttempt: 1},
		third.TurnID: {completed: true, maxAttempt: 1},
	})
	// The failed batch consumes the third budget slot: recovery must carry the
	// active settlement and the admitted second turn through a real rollover.
	id := run.GetRunID()
	rollovers, models := 0, 0
	for {
		next := ""
		it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, id, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for it.HasNext() {
			e, err := it.Next()
			require.NoError(t, err)
			if a := e.GetWorkflowExecutionContinuedAsNewEventAttributes(); a != nil {
				next = a.NewExecutionRunId
				rollovers++
			}
			if a := e.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == workflows.WebModelActivity {
				models++
			}
		}
		if next == "" {
			break
		}
		id = next
	}
	require.GreaterOrEqual(t, rollovers, 1)
	require.Equal(t, 3, models, "settlement recovery must not rerun inference")
	t.Log("real SQL outage exhausted 5 Temporal persistence attempts; admitted turn B and dispatcher command C recovered without reset")
}
func TestClosedSessionAdmissionIsTerminalAndPriorReceiptSurvives(t *testing.T) {
	f := setup(t)
	f.start(t, f.repo, mock.New())
	c := f.conversation(t, "qwen3.5:9b")
	run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 1})
	require.NoError(t, err)
	a := f.command(t, c, "accepted before close")
	d := onlyConversation(t, f, c)
	dispatchOne(t, f, d)
	f.completed(t, a)
	desc, err := f.sdk.DescribeWorkflowExecution(f.ctx, c.WorkflowID, "")
	require.NoError(t, err)
	require.NotEqual(t, run.GetRunID(), desc.WorkflowExecutionInfo.Execution.RunId)
	require.NoError(t, f.sdk.TerminateWorkflow(f.ctx, c.WorkflowID, "", "test definitive close"))
	old, err := f.gateway.Submit(f.ctx, a)
	assert.NoError(t, err)
	assert.Equal(t, "accepted", old.State, "ambiguous retry of previously completed Update retains its receipt")
	b := f.command(t, c, "new after close")
	dispatchOne(t, f, d)
	r, err := f.repo.GetCommand(f.ctx, c.ID, b.CommandID)
	require.NoError(t, err)
	require.Equal(t, "rejected", r.ResultState)
	var result chat.AdmissionResult
	require.NoError(t, json.Unmarshal(r.Result, &result))
	require.Equal(t, "conversation_closed", result.Code)
}
