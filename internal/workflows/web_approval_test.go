package workflows_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func approvalFixture(t *testing.T) (*testsuite.TestWorkflowEnvironment, chat.ConversationID, chat.SessionCommand, *atomic.Int32) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	calls := new(atomic.Int32)
	action := chat.WebAction{Scope: chat.ApprovalScope{ConversationID: c, TurnID: cmd.TurnID, ToolCallID: uuid.NewString(), ToolsetVersion: chat.WebToolset, ToolName: "web_fetch", ToolVersion: "1", ActionHash: strings.Repeat("a", 64), ContentRef: strings.Repeat("b", 64)}}
	var models atomic.Int32
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) (chat.ModelStepResult, error) {
		if models.Add(1) == 1 {
			return chat.ModelStepResult{Actions: []chat.WebAction{action}}, nil
		}
		return chat.ModelStepResult{}, nil
	}, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.WebApprovalRecord) error { return nil }, activity.RegisterOptions{Name: "RecordWebApproval"})
	e.RegisterActivityWithOptions(func(context.Context, chat.WebAction) error { return nil }, activity.RegisterOptions{Name: "RecordWebAuthorization"})
	e.RegisterActivityWithOptions(func(context.Context, chat.WebToolExecution) error { calls.Add(1); return nil }, activity.RegisterOptions{Name: "ExecuteWebTool"})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(cmd.CommandID), t, cmd) }, 0)
	return e, c, cmd, calls
}
func pendingApproval(t *testing.T, e *testsuite.TestWorkflowEnvironment) *chat.WebApproval {
	v, err := e.QueryWorkflow("state")
	require.NoError(t, err)
	var raw json.RawMessage
	require.NoError(t, v.Get(&raw))
	var s struct{ PendingApproval *chat.WebApproval }
	require.NoError(t, json.Unmarshal(raw, &s))
	return s.PendingApproval
}
func decideCommand(c chat.ConversationID, p *chat.WebApproval, d chat.ApprovalDecision) chat.SessionCommand {
	cmd := webCommand(c, 2)
	cmd.TurnID = ""
	cmd.Kind = chat.CommandDecide
	cmd.Payload = chat.CommandPayload{ApprovalID: p.ID, ActionHash: p.Scope.ActionHash, Decision: d}
	cmd.PayloadDigest, _ = (chat.CommandInput{ConversationID: c, RequestID: cmd.RequestID, Kind: cmd.Kind, Payload: cmd.Payload}).Digest()
	return cmd
}
func TestApproveOnceBoundToAction(t *testing.T) {
	e, c, _, calls := approvalFixture(t)
	e.RegisterDelayedCallback(func() {
		p := pendingApproval(t, e)
		require.NotNil(t, p)
		bad := decideCommand(c, p, chat.ApprovalOnce)
		bad.Payload.ActionHash = strings.Repeat("c", 64)
		bad.PayloadDigest, _ = (chat.CommandInput{ConversationID: c, RequestID: bad.RequestID, Kind: bad.Kind, Payload: bad.Payload}).Digest()
		e.UpdateWorkflow("decide", string(bad.CommandID), &testsuite.TestUpdateCallback{OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, "rejected", v.(chat.AdmissionResult).State)
		}, OnReject: func(err error) { t.Error(err) }}, bad)
	}, time.Second)
	e.RegisterDelayedCallback(func() {
		require.Zero(t, calls.Load())
		p := pendingApproval(t, e)
		require.NotNil(t, p)
		good := decideCommand(c, p, chat.ApprovalOnce)
		e.UpdateWorkflowNoRejection("decide", string(good.CommandID), t, good)
	}, 2*time.Second)
	e.RegisterDelayedCallback(e.CancelWorkflow, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
	require.EqualValues(t, 1, calls.Load())
}
func TestExpiryDeniesExecution(t *testing.T) {
	e, c, _, calls := approvalFixture(t)
	var outcome string
	e.RegisterActivityWithOptions(func(_ context.Context, a chat.WebAction) error { outcome = a.Outcome; return nil }, activity.RegisterOptions{Name: "RecordWebAuthorization", DisableAlreadyRegisteredCheck: true})
	e.RegisterDelayedCallback(func() { require.NotNil(t, pendingApproval(t, e)) }, time.Hour)
	e.RegisterDelayedCallback(func() {
		require.Nil(t, pendingApproval(t, e))
		require.Equal(t, "expired", outcome)
		require.Zero(t, calls.Load())
		e.CancelWorkflow()
	}, 25*time.Hour)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
	require.Zero(t, calls.Load())
}

func TestApproveOnceIsNotReusedAfterApprovalPersistenceRecovery(t *testing.T) {
	e, c, cmd, effects := approvalFixture(t)
	var models atomic.Int32
	var writes atomic.Int32
	makeAction := func() chat.WebAction {
		return chat.WebAction{Scope: chat.ApprovalScope{ConversationID: c, TurnID: cmd.TurnID, ToolCallID: uuid.NewString(), ToolsetVersion: chat.WebToolset, ToolName: "web_fetch", ToolVersion: "1", ActionHash: strings.Repeat("a", 64), ContentRef: strings.Repeat("b", 64)}}
	}
	actions := []chat.WebAction{makeAction(), makeAction()}
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) (chat.ModelStepResult, error) {
		if models.Add(1) == 1 {
			return chat.ModelStepResult{Actions: actions}, nil
		}
		return chat.ModelStepResult{}, nil
	}, activity.RegisterOptions{Name: w.WebModelActivity, DisableAlreadyRegisteredCheck: true})
	e.RegisterActivityWithOptions(func(_ context.Context, r chat.WebApprovalRecord) error {
		if r.Outcome == "authorized" && writes.Add(1) <= 5 {
			return fmt.Errorf("lost approval projection acknowledgment")
		}
		return nil
	}, activity.RegisterOptions{Name: "RecordWebApproval", DisableAlreadyRegisteredCheck: true})
	var first string
	e.RegisterDelayedCallback(func() {
		p := pendingApproval(t, e)
		require.NotNil(t, p)
		first = p.ID
		cmd := decideCommand(c, p, chat.ApprovalOnce)
		e.UpdateWorkflowNoRejection("decide", string(cmd.CommandID), t, cmd)
	}, time.Second)
	e.RegisterDelayedCallback(func() {
		p := pendingApproval(t, e)
		require.NotNil(t, p)
		require.NotEqual(t, first, p.ID)
		require.Zero(t, effects.Load(), "first once cannot authorize the second call after a failed projection batch")
		e.CancelWorkflow()
	}, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
}

func TestWebApprovalCarryRejectsUnboundPendingScope(t *testing.T) {
	e, c := setupWeb(t)
	turn := chat.TurnID(uuid.NewString())
	scope := chat.ApprovalScope{ConversationID: c, TurnID: turn, ToolCallID: uuid.NewString(), ToolsetVersion: chat.WebToolset, ToolName: "web_fetch", ToolVersion: "1", ActionHash: strings.Repeat("a", 64), ContentRef: strings.Repeat("b", 64)}
	pending := scope
	pending.ToolCallID = uuid.NewString()
	var writes atomic.Int32
	e.RegisterActivityWithOptions(func(context.Context, chat.WebApprovalRecord) error { writes.Add(1); return nil }, activity.RegisterOptions{Name: "RecordWebApproval"})
	e.RegisterDelayedCallback(e.CancelWorkflow, time.Second)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Carry: &w.WebSessionState{Status: "recovering", ActiveTurn: turn, RecoveryPhase: "tools", Batch: []chat.WebAction{{Scope: scope}}, PendingApproval: &chat.WebApproval{ID: uuid.NewString(), Scope: pending, Deadline: time.Now().Add(24 * time.Hour)}}})
	require.Zero(t, writes.Load(), "carry must bind the pending request to its exact unprocessed action")
	require.ErrorContains(t, e.GetWorkflowError(), "invalid carry")
}

func TestNewToolNotCoveredByOldGrant(t *testing.T) {
	e, c, _, calls := approvalFixture(t)
	e.RegisterDelayedCallback(func() {
		require.NotNil(t, pendingApproval(t, e), "a grant for an older registered set cannot expand to this version")
		require.Zero(t, calls.Load())
		e.CancelWorkflow()
	}, time.Second)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Carry: &w.WebSessionState{Policy: chat.WebPolicy{Revision: 8, GrantedToolset: "web-v0: web_fetch@1"}}})
}

// A revoke must be adjudicated by the real session Update handler, not merely
// accepted by the API outbox or a standalone policy helper.
func TestRevokeStopsFutureAuthorization(t *testing.T) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	cmd.Kind = chat.CommandRevoke
	cmd.TurnID = ""
	cmd.Payload = chat.CommandPayload{}
	cmd.PayloadDigest, _ = (chat.CommandInput{ConversationID: c, RequestID: cmd.RequestID, Kind: cmd.Kind}).Digest()
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("revoke", string(cmd.CommandID), &testsuite.TestUpdateCallback{OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, "accepted", v.(chat.AdmissionResult).State, "revoke is a durable policy Update")
		}, OnReject: func(err error) { t.Error(err) }}, cmd)
	}, 0)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b"})
}
