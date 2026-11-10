package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// Observations and Update callbacks run on the testsuite workflow thread.
// Activity functions only consume immutable inputs.
type afterRunFixture struct {
	t                                          *testing.T
	env                                        *testsuite.TestWorkflowEnvironment
	models, commands, approvals, ends, settled int
	pairs                                      []AppendInput
	onStart                                    func(string)
	onEnd                                      func()
	decision                                   string
}

func newAfterRunFixture(t *testing.T, model func(LLMInput) (LLMResult, error), command func(ToolInput) (CommandResult, error)) *afterRunFixture {
	var suite testsuite.WorkflowTestSuite
	f := &afterRunFixture{t: t, env: suite.NewTestWorkflowEnvironment(), decision: "approve"}
	f.env.RegisterActivityWithOptions(func(context.Context, EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: EmitActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, EmitInput) error { return nil }, activity.RegisterOptions{Name: NotifyActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, AppendInput) (AppendResult, error) {
		return AppendResult{TranscriptRef: "ref"}, nil
	}, activity.RegisterOptions{Name: TranscriptActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, GatherInput) (AppendResult, error) {
		return AppendResult{TranscriptRef: "ref"}, nil
	}, activity.RegisterOptions{Name: GatherActivity})
	f.env.RegisterActivityWithOptions(func(_ context.Context, in LLMInput) (LLMResult, error) { return model(in) }, activity.RegisterOptions{Name: LLMActivity})
	f.env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (PreflightResult, error) {
		var registry tools.LocalRegistry
		def, _ := registry.Lookup(in.Call.Name)
		hash, err := tools.ActionHash(in.Call)
		return PreflightResult{Allowed: true, Mode: def.ExecutionMode, Replay: def.Replay, ActionHash: hash}, err
	}, activity.RegisterOptions{Name: PreflightActivity})
	f.env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (tools.Result, error) {
		return tools.Result{ToolCallID: in.Call.ID, Output: "written"}, nil
	}, activity.RegisterOptions{Name: ToolActivity})
	f.env.RegisterActivityWithOptions(func(ctx context.Context, in ToolInput) (CommandResult, error) {
		if activity.GetInfo(ctx).Attempt != 1 {
			t.Error("unsafe command retried")
		}
		return command(in)
	}, activity.RegisterOptions{Name: CommandActivity})
	f.env.RegisterActivityWithOptions(func(context.Context, ToolInput) (CommandResult, error) {
		return CommandResult{ExitCode: -1, Interrupted: true, LogRef: "logs/recovered", Tail: "partial"}, nil
	}, activity.RegisterOptions{Name: RecoverCommandActivity})
	f.env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		name := info.ActivityType.Name
		switch name {
		case LLMActivity:
			f.models++
		case CommandActivity:
			f.commands++
			var in ToolInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			if in.InputID == "" || in.HangTimeout != 17*time.Second || string(in.Call.Args) != `{"cmd":"go","args":["test","./..."]}` {
				t.Errorf("command propagation: %+v", in)
			}
			if len(f.state().ActiveTools) != 1 {
				t.Error("verification not observable in ActiveTools")
			}
		case NotifyActivity:
			p := f.state().PendingApproval
			if p == nil {
				t.Fatal("missing approval")
			}
			if p.Call.Name == "run_command" {
				f.approvals++
				if len(f.state().ActiveTools) != 1 {
					t.Error("missing blocked ActiveTools")
				}
			}
			mode := f.decision
			if p.Call.Name != "run_command" {
				mode = "approve"
			}
			if mode == "approve" || mode == "deny" {
				f.env.UpdateWorkflowNoRejection(DecideUpdate, p.RequestID, t, DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: mode == "approve", User: "human"})
			} else if mode == "stop" {
				f.env.UpdateWorkflowNoRejection(StopUpdate, "stop", t)
			}
		case TranscriptActivity:
			var in AppendInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			if len(in.Messages) > 0 && in.Messages[0].Role == "assistant" {
				f.pairs = append(f.pairs, in)
			}
		case EmitActivity:
			var in EmitInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			if in.Type == events.AgentEnd {
				f.ends++
				if f.onEnd != nil {
					f.onEnd()
				}
			}
			if in.Type == events.AgentSettled {
				f.settled++
				if f.state().Inbox != nil && !f.state().QueuePaused && len(f.state().Inbox) > 0 {
					t.Error("early agent_settled")
				}
			}
		}
		if f.onStart != nil {
			f.onStart(name)
		}
	})
	return f
}
func (f *afterRunFixture) state() State {
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
func (f *afterRunFixture) send(id string) {
	f.env.UpdateWorkflowNoRejection(MessageUpdate, id, f.t, MessageInput{Text: id, RequestID: id})
}
func (f *afterRunFixture) run(input Input, check func()) {
	input.HangTimeout = 17 * time.Second
	input.ApprovalTimeout = time.Second
	checked := false
	f.env.RegisterDelayedCallback(func() { checked = true; check(); f.env.CancelWorkflow() }, time.Minute)
	f.env.ExecuteWorkflow(SessionWorkflow, input)
	if !checked || !temporal.IsCanceledError(f.env.GetWorkflowError()) {
		f.t.Fatalf("missing checkpoint/cancellation: %v", f.env.GetWorkflowError())
	}
}
func writeThenAnswer(in LLMInput) (LLMResult, error) {
	if in.InputID == runInputID(1, "original") && in.Turn == 0 {
		return LLMResult{ToolCalls: []llm.ToolCall{{ID: "write", Name: "write_file", Args: []byte(`{"path":"x","content":"updated"}`)}}}, nil
	}
	return LLMResult{Summary: "model answer"}, nil
}
func testCommand(exit int) CommandResult {
	return CommandResult{ExitCode: exit, LogRef: "logs/test.log", Tail: "FAIL failing assertion"}
}
func (f *afterRunFixture) assertPairs() {
	f.t.Helper()
	for _, in := range f.pairs {
		if len(in.Messages) != 2 {
			f.t.Fatalf("non-atomic pair: %+v", in)
		}
		a, b := in.Messages[0], in.Messages[1]
		if len(a.ToolCalls) != 1 || b.Role != "tool" || a.ToolCalls[0].ID != b.ToolCallID || b.ToolName != "run_command" {
			f.t.Errorf("incoherent pair: %+v", in)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(b.Content), &body); err != nil {
			f.t.Fatal(err)
		}
		for _, key := range []string{"exitCode", "isError", "interrupted", "logRef", "tail"} {
			if _, ok := body[key]; !ok {
				f.t.Errorf("missing %s: %s", key, b.Content)
			}
		}
	}
}
func TestAfterRunWriteVerificationAndBudget(t *testing.T) {
	for _, failures := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			f := newAfterRunFixture(t, writeThenAnswer, func(in ToolInput) (CommandResult, error) {
				exit := 0
				// Each verification belongs to a distinct logical run, not an activity retry.
				for i := 1; i <= failures; i++ {
					if strings.HasPrefix(in.InputID, fmt.Sprintf("run/%d/", i)) {
						exit = 1
					}
				}
				return testCommand(exit), nil
			})
			f.onEnd = func() {
				if f.ends <= failures && f.ends < 3 {
					s := f.state()
					if len(s.Inbox) != 1 {
						t.Fatalf("missing automatic fix: %+v", s)
					}
					q := s.Inbox[0]
					if !q.Internal || !q.VerificationPending || q.AfterRunCount != f.ends || q.OriginInputID != runInputID(1, "original") || !strings.Contains(q.Text, "FAIL failing assertion") || !strings.Contains(q.Text, "logs/test.log") {
						t.Errorf("queued provenance: %+v", q)
					}
					if f.settled != 0 {
						t.Error("settled before entire fix chain")
					}
				}
			}
			f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
			f.run(Input{}, func() {
				s := f.state()
				want := failures + 1
				if want > 3 {
					want = 3
				}
				if f.commands != want || f.approvals != want || f.models != want+1 || f.ends != want || f.settled != 1 || len(f.pairs) != want {
					t.Errorf("counts commands=%d approvals=%d models=%d ends=%d settled=%d pairs=%d", f.commands, f.approvals, f.models, f.ends, f.settled, len(f.pairs))
				}
				if s.AfterRunCount != want-1 || s.OriginInputID != runInputID(1, "original") {
					t.Errorf("provenance: %+v", s)
				}
				if failures == 3 {
					if s.LastRunFailure == nil || s.LastRunFailure.Reason != "restart_limit" || !s.QueuePaused || !s.HasUnansweredContext {
						t.Errorf("limit: %+v", s)
					}
				} else if s.LastRunFailure != nil || s.HasUnansweredContext || !strings.Contains(s.Answer, "go test ./... passed") {
					t.Errorf("success: %+v", s)
				}
				f.assertPairs()
			})
		})
	}
}
func TestAfterRunNoWriteSkip(t *testing.T) {
	f := newAfterRunFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "answer"}, nil }, func(ToolInput) (CommandResult, error) { t.Error("unexpected command"); return CommandResult{}, nil })
	f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
	f.run(Input{}, func() {
		if f.commands != 0 || f.approvals != 0 || len(f.pairs) != 0 || f.models != 1 {
			t.Error("no-write verification")
		}
	})
}
func TestAfterRunDeniedTimeoutStopNeverExecute(t *testing.T) {
	for _, mode := range []string{"deny", "timeout", "stop"} {
		t.Run(mode, func(t *testing.T) {
			f := newAfterRunFixture(t, writeThenAnswer, func(ToolInput) (CommandResult, error) { t.Error("unapproved execution"); return CommandResult{}, nil })
			f.decision = mode
			f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
			f.run(Input{}, func() {
				s := f.state()
				if f.commands != 0 || f.models != 2 || f.approvals != 1 || len(f.pairs) != 1 || s.LastRunFailure == nil || !s.QueuePaused || !s.HasUnansweredContext || s.AfterRunCount != 0 {
					t.Errorf("unsafe retry/settlement: %+v commands=%d models=%d pairs=%d", s, f.commands, f.models, len(f.pairs))
				}
				f.assertPairs()
			})
		})
	}
}
