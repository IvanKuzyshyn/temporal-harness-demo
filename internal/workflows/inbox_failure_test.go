package workflows

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestInboxStopRetainsAndContinueJoinsQueue(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "ok"}, nil })
	f.env.OnActivity(LLMActivity, mock.Anything, mock.MatchedBy(func(in LLMInput) bool { return in.InputID == runInputID(1, "first") })).After(time.Hour).Return(LLMResult{Summary: "stale"}, nil)
	f.env.OnActivity(LLMActivity, mock.Anything, mock.MatchedBy(func(in LLMInput) bool { return in.InputID != runInputID(1, "first") })).Return(LLMResult{Summary: "ok"}, nil)
	f.env.RegisterDelayedCallback(func() { f.send("first", "first", "") }, 0)
	f.after(func() { f.send("queued", "queued", "followUp"); f.env.UpdateWorkflowNoRejection(StopUpdate, "stop", t) }, time.Second)
	f.after(func() {
		s := f.state()
		if s.Status != "idle" || s.LastRunFailure == nil || s.LastRunFailure.Reason != "aborted" || !s.HasUnansweredContext || !s.QueuePaused || len(s.Inbox) != 1 {
			t.Errorf("stop state=%+v", s)
		}
	}, 2*time.Second)
	f.after(func() {
		if len(f.users) != 1 {
			t.Errorf("queue auto-ran: %v", f.users)
		}
		f.env.UpdateWorkflowNoRejection(ContinueUpdate, "continue", t)
	}, 3*time.Second)
	f.after(func() {
		s := f.state()
		if s.RunCounter != 3 || s.Answer != "ok" || s.LastRunFailure != nil || s.HasUnansweredContext || fmt.Sprint(f.users) != "[first queued]" {
			t.Errorf("resume state=%+v users=%v", s, f.users)
		}
		f.env.CancelWorkflow()
	}, 4*time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
	if !temporal.IsCanceledError(f.env.GetWorkflowError()) {
		t.Errorf("global cancel did not exit: %v", f.env.GetWorkflowError())
	}
}
func TestInboxTurnBudgetFinalVersusToolsAndSteer(t *testing.T) {
	for _, kind := range []string{"final", "tools", "steer"} {
		t.Run(kind, func(t *testing.T) {
			f := newInboxFixture(t, func(LLMInput) (LLMResult, error) {
				if kind == "tools" {
					return LLMResult{ToolCalls: []llm.ToolCall{{ID: "call", Name: "read_file", Args: []byte(`{"path":"x"}`)}}}, nil
				}
				return LLMResult{Summary: "last-turn answer"}, nil
			})
			f.onModel = func(n int) {
				if kind == "steer" {
					f.send("steer", "steer", "steer")
				}
			}
			f.after(func() { f.send("first", "first", "") }, 0)
			f.after(func() {
				s := f.state()
				if f.models != 1 || s.Turn != 1 {
					t.Errorf("budget state=%+v models=%d", s, f.models)
				}
				if kind == "final" {
					if s.LastRunFailure != nil || s.Answer != "last-turn answer" || s.HasUnansweredContext {
						t.Errorf("final=%+v", s)
					}
				} else if s.LastRunFailure == nil || s.LastRunFailure.Reason != "turn_limit" || s.HasUnansweredContext != (kind == "tools") || !s.QueuePaused {
					t.Errorf("limit=%+v", s)
				}
				if kind == "steer" && (len(s.Inbox) != 1 || s.Inbox[0].WhenBusy == "steer") {
					t.Errorf("lost/busy retained steer=%+v", s.Inbox)
				}
				f.env.CancelWorkflow()
			}, time.Second)
			f.env.ExecuteWorkflow(SessionWorkflow, Input{MaxTurns: 1})
		})
	}
}
func TestInboxInterruptedToolStopsLogicalRun(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) {
		return LLMResult{ToolCalls: []llm.ToolCall{{ID: "call", Name: "read_file", Args: []byte(`{"path":"x"}`)}}}, nil
	})
	// The batch contract marks an unsafe timeout/recovery Interrupted. Exercise
	// that contract without changing the independently owned approval stub.
	f.env.RegisterActivityWithOptions(func(context.Context, ToolInput) (tools.Result, error) {
		return tools.Result{ToolCallID: "call", Interrupted: true, IsError: true, Output: "partial"}, nil
	}, activity.RegisterOptions{Name: ToolActivity})
	f.env.OnActivity(PreflightActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, in ToolInput) (PreflightResult, error) {
		var reg tools.LocalRegistry
		def, _ := reg.Lookup(in.Call.Name)
		return PreflightResult{Allowed: true, Mode: def.ExecutionMode, Replay: def.Replay, ActionHash: "hash"}, nil
	})
	f.onModel = func(n int) {
		if n == 1 {
			f.send("queued", "queued", "followUp")
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("first", "first", "") }, 0)
	f.after(func() {
		s := f.state()
		if f.models != 1 || s.LastRunFailure == nil || s.LastRunFailure.Reason != "interrupted" || len(s.Inbox) != 1 || !s.QueuePaused || fmt.Sprint(f.roles) != "[user tool]" {
			t.Errorf("state=%+v models=%d roles=%v", s, f.models, f.roles)
		}
		f.env.CancelWorkflow()
	}, time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
}
func TestInboxStopDuringTools(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) {
		return LLMResult{ToolCalls: []llm.ToolCall{{ID: "a", Name: "read_file", Args: []byte(`{"path":"a"}`)}, {ID: "b", Name: "read_file", Args: []byte(`{"path":"b"}`)}}}, nil
	})
	f.env.RegisterActivityWithOptions(func(context.Context, ToolInput) (tools.Result, error) { return tools.Result{}, nil }, activity.RegisterOptions{Name: ToolActivity})
	f.env.OnActivity(PreflightActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, in ToolInput) (PreflightResult, error) {
		var reg tools.LocalRegistry
		def, _ := reg.Lookup(in.Call.Name)
		return PreflightResult{Allowed: true, Mode: def.ExecutionMode, Replay: def.Replay, ActionHash: "hash"}, nil
	})
	f.env.OnActivity(ToolActivity, mock.Anything, mock.Anything).After(time.Hour).Return(tools.Result{Output: "stale"}, nil)
	f.env.RegisterDelayedCallback(func() { f.send("first", "first", "") }, 0)
	f.after(func() {
		if len(f.state().ActiveTools) != 2 {
			t.Errorf("tools did not start: %+v", f.state())
		}
		f.send("queued", "queued", "followUp")
		f.env.UpdateWorkflowNoRejection(StopUpdate, "stop", t)
	}, time.Second)
	f.after(func() {
		s := f.state()
		if s.Status != "idle" || s.LastRunFailure == nil || s.LastRunFailure.Reason != "aborted" || len(s.ActiveTools) != 0 || len(s.Inbox) != 1 || f.models != 1 {
			t.Errorf("tool stop=%+v models=%d", s, f.models)
		}
		f.env.CancelWorkflow()
	}, 2*time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
}

func TestInboxInternalFollowUpKeepsOriginAndCounter(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "fixed"}, nil })
	f.after(func() {
		s := f.state()
		if s.AfterRunCount != 2 || s.OriginInputID != "original" || s.WroteFile {
			t.Errorf("internal provenance=%+v", s)
		}
		f.send("genuine", "new user", "")
	}, time.Second)
	f.after(func() {
		s := f.state()
		if s.AfterRunCount != 0 || s.OriginInputID != runInputID(6, "genuine") {
			t.Errorf("genuine provenance=%+v", s)
		}
		f.env.CancelWorkflow()
	}, 2*time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{Carry: &State{Status: "idle", RunCounter: 4, AfterRunCount: 2, WroteFile: true, OriginInputID: "original", Inbox: []MessageInput{{Text: "test failure", Internal: true, OriginInputID: "original", AfterRunCount: 2, WhenBusy: "followUp"}}}})
}
func TestInboxSteerDuringFinalTurnEnd(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "answer"}, nil })
	f.onEmit = func(typ events.Type) {
		if typ == events.TurnEnd && f.models == 1 {
			f.send("steer", "steer", "steer")
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("first", "first", "") }, 0)
	f.after(func() {
		s := f.state()
		if f.models != 2 || s.RunCounter != 1 || s.Turn != 2 || s.HasUnansweredContext || fmt.Sprint(f.users) != "[first steer]" {
			t.Errorf("final-turn steer state=%+v users=%v", s, f.users)
		}
		f.env.CancelWorkflow()
	}, time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
}

func TestInboxLateSteerQueuesAfterAgentEnd(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "answer"}, nil })
	// Enqueue at the exact settlement yield on the workflow-thread listener.
	f.onEmit = func(typ events.Type) {
		if typ == events.AgentEnd && len(f.users) == 1 {
			f.env.UpdateWorkflow(MessageUpdate, "late", &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
				if err != nil || v.(MessageReceipt).Disposition != "queued" {
					t.Errorf("late receipt=%v error=%v", v, err)
				}
			}}, MessageInput{Text: "late", RequestID: "late", WhenBusy: "steer"})
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("first", "first", "") }, 0)
	f.after(func() {
		if f.models != 2 || f.state().RunCounter != 2 || fmt.Sprint(f.users) != "[first late]" {
			t.Errorf("late state=%+v users=%v", f.state(), f.users)
		}
		f.env.CancelWorkflow()
	}, time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
}
