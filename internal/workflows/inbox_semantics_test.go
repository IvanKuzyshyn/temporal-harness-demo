package workflows

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// Activity-start listeners execute on the testsuite workflow thread. Keep all
// assertions/observations there, rather than sharing slices with activity goroutines.
type inboxFixture struct {
	t       *testing.T
	env     *testsuite.TestWorkflowEnvironment
	users   []string
	roles   []string
	events  []events.Type
	models  int
	onModel func(int)
	onEmit  func(events.Type)
	// Cancellation is the default terminal; CAN tests opt in explicitly.
	expectCAN bool
	scheduled int
	checked   int
}

func newInboxFixture(t *testing.T, model func(LLMInput) (LLMResult, error)) *inboxFixture {
	var suite testsuite.WorkflowTestSuite
	f := &inboxFixture{t: t, env: suite.NewTestWorkflowEnvironment()}
	t.Cleanup(func() {
		if !f.env.IsWorkflowCompleted() {
			t.Error("test did not complete its workflow")
			return
		}
		if f.checked != f.scheduled {
			t.Errorf("skipped assertion checkpoints: ran %d of %d", f.checked, f.scheduled)
		}
		err := f.env.GetWorkflowError()
		var can *workflow.ContinueAsNewError
		if f.expectCAN {
			if !errors.As(err, &can) {
				t.Errorf("expected CAN terminal, got %v", err)
			}
		} else if !temporal.IsCanceledError(err) {
			t.Errorf("expected cancellation after checkpoints, got %v", err)
		}
	})
	f.env.RegisterActivityWithOptions(func(context.Context, EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: EmitActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, AppendInput) (AppendResult, error) {
		return AppendResult{TranscriptRef: "ref"}, nil
	}, activity.RegisterOptions{Name: TranscriptActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, GatherInput) (AppendResult, error) {
		return AppendResult{TranscriptRef: "ref"}, nil
	}, activity.RegisterOptions{Name: GatherActivity})
	f.env.RegisterActivityWithOptions(func(_ context.Context, in LLMInput) (LLMResult, error) { return model(in) }, activity.RegisterOptions{Name: LLMActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, ToolInput) (PreflightResult, error) {
		return PreflightResult{Reason: "denied"}, nil
	}, activity.RegisterOptions{Name: PreflightActivity})
	f.env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		switch info.ActivityType.Name {
		case TranscriptActivity:
			var in AppendInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			for _, m := range in.Messages {
				f.roles = append(f.roles, m.Role)
				if m.Role == "user" {
					f.users = append(f.users, m.Content)
				}
			}
		case EmitActivity:
			var in EmitInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			f.events = append(f.events, in.Type)
			if f.onEmit != nil {
				f.onEmit(in.Type)
			}
		case LLMActivity:
			f.models++
			if f.onModel != nil {
				f.onModel(f.models)
			}
		}
	})
	return f
}

// after requires every scheduled scenario/assertion callback to execute, even
// if an erroneous early terminal would otherwise be accepted by the fixture.
func (f *inboxFixture) after(fn func(), delay time.Duration) {
	f.scheduled++
	f.env.RegisterDelayedCallback(func() {
		f.checked++
		fn()
	}, delay)
}
func (f *inboxFixture) send(id, text, mode string) {
	f.env.UpdateWorkflowNoRejection(MessageUpdate, id, f.t, MessageInput{Text: text, RequestID: id, WhenBusy: mode})
}
func (f *inboxFixture) state() State {
	v, err := f.env.QueryWorkflow(StateQuery)
	if err != nil {
		f.t.Fatal(err)
	}
	var s State
	if err = v.Get(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}
func TestInboxActualSteerAndFollowUp(t *testing.T) {
	for _, tools := range []bool{false, true} {
		t.Run(fmt.Sprint(tools), func(t *testing.T) {
			f := newInboxFixture(t, func(in LLMInput) (LLMResult, error) {
				if tools && in.Turn == 0 && in.InputID == runInputID(1, "first") {
					return LLMResult{ToolCalls: []llm.ToolCall{{ID: "tool", Name: "read_file", Args: []byte(`{"path":"x"}`)}}}, nil
				}
				return LLMResult{Summary: "answer"}, nil
			})
			f.onModel = func(n int) {
				if n == 1 {
					f.send("follow1", "follow1", "followUp")
					f.send("steer1", "steer1", "steer")
					f.send("follow2", "follow2", "followUp")
					f.send("steer2", "steer2", "steer")
				}
			}
			f.after(func() { f.send("first", "first", "") }, 0)
			f.after(func() {
				if got := fmt.Sprint(f.users); got != "[first steer1 steer2 follow1 follow2]" {
					t.Errorf("users=%s", got)
				}
				if f.models != 4 || f.state().RunCounter != 3 {
					t.Errorf("models=%d state=%+v", f.models, f.state())
				}
				ends, settled := 0, 0
				for _, e := range f.events {
					if e == events.AgentEnd {
						ends++
					}
					if e == events.AgentSettled {
						settled++
						if ends != 3 {
							t.Error("early settled")
						}
					}
				}
				if settled != 1 {
					t.Errorf("settled=%d", settled)
				}
				if tools && fmt.Sprint(f.roles[:4]) != "[user tool user user]" {
					t.Errorf("tool round must precede steers: %v", f.roles)
				}
				f.env.CancelWorkflow()
			}, time.Minute)
			f.env.ExecuteWorkflow(SessionWorkflow, Input{})
		})
	}
}
func TestInboxFailedQueuePausedAndNewInputResumesOldest(t *testing.T) {
	f := newInboxFixture(t, func(in LLMInput) (LLMResult, error) {
		if in.InputID == runInputID(1, "first") {
			return LLMResult{}, temporal.NewNonRetryableApplicationError("bad model", "InvalidRequest", nil)
		}
		return LLMResult{Summary: "ok"}, nil
	})
	f.onModel = func(n int) {
		if n == 1 {
			f.send("older", "older", "followUp")
		}
	}
	f.after(func() { f.send("first", "first", "") }, 0)
	f.after(func() {
		s := f.state()
		if f.models != 1 || s.LastRunFailure == nil || s.LastRunFailure.Reason != "model_error" || len(s.Inbox) != 1 || s.Status != "idle" {
			t.Errorf("not paused: %+v calls=%d", s, f.models)
		}
		f.send("new", "new", "")
	}, time.Second)
	f.after(func() {
		if fmt.Sprint(f.users) != "[first older new]" {
			t.Errorf("users=%v", f.users)
		}
		f.env.CancelWorkflow()
	}, 2*time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
}
func TestInboxContinueBusyAndContextOnly(t *testing.T) {
	f := newInboxFixture(t, func(in LLMInput) (LLMResult, error) {
		if in.InputID == runInputID(1, "first") {
			return LLMResult{}, temporal.NewNonRetryableApplicationError("bad model", "InvalidRequest", nil)
		}
		return LLMResult{Summary: "ok"}, nil
	})
	rejected := 0
	resume := func(id string, wantError bool) {
		f.env.UpdateWorkflow(ContinueUpdate, id, &testsuite.TestUpdateCallback{OnReject: func(err error) {
			if !wantError {
				t.Error(err)
			}
			rejected++
		}, OnComplete: func(_ any, err error) {
			if (err != nil) != wantError {
				t.Errorf("continue error=%v", err)
			}
			if err != nil {
				rejected++
			}
		}})
	}
	f.onModel = func(n int) {
		if n == 1 {
			resume("busy", true)
		}
	}
	f.after(func() { resume("empty", true); f.send("first", "first", "") }, 0)
	f.after(func() { resume("resume", false) }, time.Second)
	f.after(func() {
		if f.models != 2 || fmt.Sprint(f.users) != "[first]" {
			t.Errorf("models=%d users=%v", f.models, f.users)
		}
		resume("answered", true)
	}, 2*time.Second)
	f.after(func() {
		if rejected != 3 {
			t.Errorf("rejected=%d", rejected)
		}
		if s := f.state(); s.LastRunFailure == nil || s.LastRunFailure.Reason != "nothing_to_answer" {
			t.Errorf("state=%+v", s)
		}
		f.env.CancelWorkflow()
	}, 3*time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{})
}
