package workflows_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func controlCommand(c chat.SessionCommand, n int, kind chat.CommandKind) chat.SessionCommand {
	out := webCommand(c.ConversationID, n)
	out.Kind = kind
	out.Payload.ExpectedActiveTurnID = c.TurnID
	if kind == chat.CommandStop {
		out.TurnID = ""
		out.Payload.Text = ""
	}
	out.PayloadDigest, _ = (chat.CommandInput{ConversationID: out.ConversationID, RequestID: out.RequestID, Kind: out.Kind, Payload: out.Payload}).Digest()
	return out
}

// Removing the target claim, cancellation, priority, or stale-target validation
// breaks these assertions. Test-suite cancellation is NOT server join evidence;
// the integration barrier separately verifies real activity cleanup acknowledgment.
func replacementScenario(t *testing.T, mode string) {
	e, c := setupWeb(t)
	target := webCommand(c, 1)
	follow := webCommand(c, 2)
	replace := controlCommand(target, 3, chat.CommandReplace)
	competing := controlCommand(target, 4, chat.CommandReplace)
	stop := controlCommand(target, 5, chat.CommandStop)
	var mu sync.Mutex
	var order []chat.TurnID
	var statuses []string
	receipts := map[chat.CommandID]chat.AdmissionResult{}
	e.RegisterActivityWithOptions(func(_ context.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
		mu.Lock()
		defer mu.Unlock()
		return receipts[c.CommandID], nil
	}, activity.RegisterOptions{Name: w.WebCheckCommandActivity, DisableAlreadyRegisteredCheck: true})
	e.RegisterActivityWithOptions(func(_ context.Context, a chat.Admission) error {
		mu.Lock()
		defer mu.Unlock()
		receipts[a.Command.CommandID] = a.Result
		return nil
	}, activity.RegisterOptions{Name: w.WebAdmissionActivity, DisableAlreadyRegisteredCheck: true})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: "MarkChatCancelling"})
	e.RegisterActivityWithOptions(func(ctx context.Context, r chat.TurnRef) error {
		mu.Lock()
		order = append(order, r.TurnID)
		mu.Unlock()
		if r.TurnID == target.TurnID {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(_ context.Context, r chat.TurnResult) error {
		mu.Lock()
		statuses = append(statuses, r.Status)
		mu.Unlock()
		return nil
	}, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	submit := func(cmd chat.SessionCommand, want string) {
		e.UpdateWorkflow(string(cmd.Kind), string(cmd.CommandID), &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, want, v.(chat.AdmissionResult).State)
		}}, cmd)
	}
	e.RegisterDelayedCallback(func() { submit(target, "accepted") }, 0)
	e.RegisterDelayedCallback(func() {
		if mode == "priority" {
			submit(follow, "accepted")
		}
		submit(replace, "accepted")
		if mode == "conflict" {
			submit(competing, "rejected")
		}
	}, time.Second)
	e.RegisterDelayedCallback(func() {
		if mode == "retry" {
			submit(replace, "accepted")
		}
		if mode == "stale" {
			submit(stop, "rejected")
		}
	}, 2*time.Second)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, 3*time.Second)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
	mu.Lock()
	defer mu.Unlock()
	want := []chat.TurnID{target.TurnID, replace.TurnID}
	wantStatuses := []string{"cancelled", "completed"}
	if mode == "priority" {
		want = append(want, follow.TurnID)
		wantStatuses = append(wantStatuses, "completed")
	}
	require.Equal(t, want, order)
	require.Equal(t, wantStatuses, statuses)
}
func TestReplaceJoinsBeforeStart(t *testing.T)            { replacementScenario(t, "join") }
func TestReplacementRetrySingleTurn(t *testing.T)         { replacementScenario(t, "retry") }
func TestCompetingReplacementConflict(t *testing.T)       { replacementScenario(t, "conflict") }
func TestStaleStopCannotCancelNextTurn(t *testing.T)      { replacementScenario(t, "stale") }
func TestReplacementPrecedesQueuedFollowUps(t *testing.T) { replacementScenario(t, "priority") }
