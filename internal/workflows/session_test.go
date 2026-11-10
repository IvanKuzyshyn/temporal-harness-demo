package workflows_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// Context snapshots share the parent transcript used by the session fixture.
func registerSessionContext(env *testsuite.TestWorkflowEnvironment, ref string) {
	env.RegisterActivityWithOptions(func(context.Context, w.GatherInput) (w.AppendResult, error) {
		return w.AppendResult{TranscriptRef: ref}, nil
	}, activity.RegisterOptions{Name: w.GatherActivity})
}

func TestSessionChatReceiptAndReconnect(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	registerSessionContext(env, "artifacts/transcript.json")
	var emitted []events.Type
	var calls int
	var receipt bool
	env.RegisterActivityWithOptions(func(_ context.Context, in w.EmitInput) (uint64, error) {
		emitted = append(emitted, in.Type)
		return uint64(len(emitted)), nil
	}, activity.RegisterOptions{Name: w.EmitActivity})
	env.RegisterActivityWithOptions(func(_ context.Context, in w.AppendInput) (w.AppendResult, error) {
		return w.AppendResult{TranscriptRef: "artifacts/transcript.json"}, nil
	}, activity.RegisterOptions{Name: w.TranscriptActivity})
	env.RegisterActivityWithOptions(func(_ context.Context, in w.LLMInput) (w.LLMResult, error) {
		if !receipt {
			t.Error("model invoked before Update receipt")
		}
		calls++
		if in.Workdir != "/selected/parent" {
			t.Errorf("model workdir=%q", in.Workdir)
		}
		if in.Turn != 0 {
			t.Errorf("turn=%d", in.Turn)
		}
		return w.LLMResult{Summary: "answer", MessageRef: "message-ref"}, nil
	}, activity.RegisterOptions{Name: w.LLMActivity})
	submit := func(id string) {
		env.UpdateWorkflow(w.MessageUpdate, id, &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
			if err != nil {
				t.Error(err)
			}
			if v.(w.MessageReceipt).Disposition != "started" {
				t.Errorf("receipt=%v", v)
			}
			receipt = true
		}}, w.MessageInput{Text: "hello", RequestID: id})
	}
	env.RegisterDelayedCallback(func() { submit("one") }, 0)
	env.RegisterDelayedCallback(func() {
		state := query(t, env)
		if state.Status != "idle" || state.Answer != "answer" || state.MaxTurns != 12 || state.TranscriptRef == "" {
			t.Errorf("state=%+v", state)
		}
		if env.IsWorkflowCompleted() {
			t.Error("session closed after answer")
		}
		submit("one") // SDK update ID deduplication: no second model call.
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		if calls != 1 {
			t.Errorf("duplicate ran model: %d", calls)
		}
		submit("two")
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		if calls != 2 || query(t, env).RunCounter != 2 {
			t.Errorf("reconnect calls=%d", calls)
		}
		env.CancelWorkflow()
	}, 3*time.Second)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{Workdir: "/selected/parent"})
	want := []events.Type{
		events.AgentStart, events.ParallelStart, events.ParallelEnd, events.TurnStart, events.TurnEnd, events.AgentEnd, events.AgentSettled,
		events.AgentStart, events.ParallelStart, events.ParallelEnd, events.TurnStart, events.TurnEnd, events.AgentEnd, events.AgentSettled,
	}
	if len(emitted) != len(want) {
		t.Fatalf("events=%v", emitted)
	}
	for i, typ := range emitted {
		if typ != want[i] {
			t.Errorf("event %d=%s", i, typ)
		}
	}
}

func TestSessionValidationAndFailures(t *testing.T) {
	for _, scenario := range []string{"tools", "error"} {
		t.Run(scenario, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			registerSessionContext(env, "ref")
			env.RegisterActivityWithOptions(func(_ context.Context, in w.ToolInput) (w.PreflightResult, error) {
				if in.Call.ID != "unsafe" || in.Call.Name != "write_file" {
					t.Errorf("unexpected preflight: %+v", in.Call)
				}
				var registry tools.LocalRegistry
				def, _ := registry.Lookup(in.Call.Name)
				return w.PreflightResult{Allowed: true, RequiresApproval: true, Mode: def.ExecutionMode, Replay: def.Replay, ActionHash: "write-action"}, nil
			}, activity.RegisterOptions{Name: w.PreflightActivity})
			var calls, rejected int
			var toolResults []llm.Message
			var failed bool
			env.RegisterActivityWithOptions(func(_ context.Context, in w.EmitInput) (uint64, error) {
				if in.Type == events.RunFailed {
					failed = true
				}
				return 1, nil
			}, activity.RegisterOptions{Name: w.EmitActivity})
			env.RegisterActivityWithOptions(func(_ context.Context, in w.AppendInput) (w.AppendResult, error) {
				for _, m := range in.Messages {
					if m.Role == "tool" {
						toolResults = append(toolResults, m)
					}
				}
				return w.AppendResult{TranscriptRef: "ref"}, nil
			}, activity.RegisterOptions{Name: w.TranscriptActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.LLMInput) (w.LLMResult, error) {
				calls++
				if scenario == "error" {
					return w.LLMResult{}, fmt.Errorf("model unavailable")
				}
				return w.LLMResult{ToolCalls: []llm.ToolCall{{ID: "unsafe", Name: "write_file", Args: []byte(`{"path":"x","content":"bad"}`)}}}, nil
			}, activity.RegisterOptions{Name: w.LLMActivity})
			env.RegisterDelayedCallback(func() {
				for i, msg := range []w.MessageInput{{Text: "  "}, {Text: "hello", WhenBusy: "invalid"}, {Text: "hello", WhenBusy: "followup"}} {
					env.UpdateWorkflow(w.MessageUpdate, fmt.Sprint(i), &testsuite.TestUpdateCallback{OnReject: func(error) { rejected++ }, OnComplete: func(any, error) { t.Error("invalid update completed") }}, msg)
				}
				env.UpdateWorkflowNoRejection(w.MessageUpdate, "valid", t, w.MessageInput{Text: "hello"})
			}, 0)
			env.RegisterDelayedCallback(func() {
				s := query(t, env)
				if rejected != 3 || s.Status != "idle" || s.LastRunFailure == nil || !failed {
					t.Errorf("state=%+v rejected=%d failed=%v", s, rejected, failed)
				}
				wantReason := "model_error"
				if scenario == "tools" {
					wantReason = "turn_limit"
				}
				if s.LastRunFailure == nil || s.LastRunFailure.Reason != wantReason {
					t.Errorf("failure=%+v want %s", s.LastRunFailure, wantReason)
				}
				if scenario == "tools" {
					if calls != 2 || len(toolResults) != 2 {
						t.Errorf("calls=%d results=%v", calls, toolResults)
					}
					for _, m := range toolResults {
						if m.ToolCallID != "unsafe" || !strings.Contains(m.Content, "tool approval denied") {
							t.Error(m)
						}
					}
				}
				env.CancelWorkflow()
			}, time.Minute)
			env.ExecuteWorkflow(w.SessionWorkflow, w.Input{MaxTurns: 2})
		})
	}
}

func query(t *testing.T, env *testsuite.TestWorkflowEnvironment) w.State {
	t.Helper()
	v, err := env.QueryWorkflow(w.StateQuery)
	if err != nil {
		t.Fatal(err)
	}
	var s w.State
	if err = v.Get(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
