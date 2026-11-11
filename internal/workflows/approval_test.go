package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestApprovalGate(t *testing.T) {
	for _, mode := range []string{"approve", "deny", "timeout", "cancel", "display failure", "preflight mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			call := llm.ToolCall{ID: "write", Name: "write_file", Args: []byte(`{"path":"x","content":"new"}`)}
			hash, _ := tools.ActionHash(call)
			var writes atomic.Int32
			var authorized atomic.Bool
			var rejected, completed int
			var resolved events.ApprovalData
			env.RegisterActivityWithOptions(func(context.Context, ToolInput) (PreflightResult, error) {
				h := hash
				if mode == "preflight mismatch" {
					h = "wrong"
				}
				return PreflightResult{Allowed: true, RequiresApproval: true, Mode: tools.Sequential, Replay: tools.Unsafe, ActionHash: h, Preview: "-old\n+new"}, nil
			}, activity.RegisterOptions{Name: PreflightActivity})
			env.RegisterActivityWithOptions(func(context.Context, EmitInput) error {
				if mode == "display failure" {
					return fmt.Errorf("display unavailable")
				}
				return nil
			}, activity.RegisterOptions{Name: NotifyActivity})
			env.RegisterActivityWithOptions(func(_ context.Context, in EmitInput) (uint64, error) {
				if in.Type == events.ApprovalResolved {
					b, _ := json.Marshal(in.Data)
					if err := json.Unmarshal(b, &resolved); err != nil {
						t.Error(err)
					}
				}
				return 0, nil
			}, activity.RegisterOptions{Name: EmitActivity})
			env.RegisterActivityWithOptions(func(context.Context, ToolInput) (tools.Result, error) {
				if !authorized.Load() {
					t.Error("write_file executed before valid approve completed")
				}
				writes.Add(1)
				return tools.Result{Output: "written"}, nil
			}, activity.RegisterOptions{Name: ToolActivity})
			wf := func(ctx workflow.Context) (BatchResult, error) {
				runCtx, cancel := workflow.WithCancel(ctx)
				r := runtime{cfg: Input{ApprovalTimeout: 3 * time.Second}, state: State{Status: "working"}, cancel: cancel}
				if err := r.registerApprovalHandlers(ctx); err != nil {
					return BatchResult{}, err
				}
				_ = workflow.SetQueryHandler(ctx, StateQuery, func() (State, error) { return r.state, nil })
				_ = workflow.SetUpdateHandler(ctx, StopUpdate, r.stop)
				result, err := r.executeBatch(runCtx, []llm.ToolCall{call})
				if r.state.PendingApproval != nil || r.state.Status != "working" {
					return result, fmt.Errorf("approval state leaked: %+v", r.state)
				}
				return result, err
			}
			if mode != "preflight mismatch" {
				env.RegisterDelayedCallback(func() {
					if writes.Load() != 0 {
						t.Error("write_file dispatched before valid decide")
					}
					v, err := env.QueryWorkflow(StateQuery)
					if err != nil {
						t.Fatal(err)
					}
					var s State
					if err = v.Get(&s); err != nil {
						t.Fatal(err)
					}
					p := s.PendingApproval
					if p == nil {
						t.Fatal("missing pending approval")
					}
					if s.Status != "blocked" || p.ActionHash != hash || !strings.HasSuffix(p.RequestID, "-approval-1") || p.Preview != "-old\n+new" {
						t.Fatalf("pending=%+v state=%s", p, s.Status)
					}
					good := DecisionInput{RequestID: p.RequestID, ActionHash: hash, Approved: mode != "deny", User: "alice", Reason: "unsafe"}
					for i, bad := range []DecisionInput{
						{RequestID: "stale", ActionHash: hash, User: "alice"},
						{RequestID: p.RequestID, ActionHash: "bad", User: "alice"},
						{RequestID: p.RequestID, ActionHash: hash, User: "  "},
					} {
						env.UpdateWorkflow(DecideUpdate, fmt.Sprint("bad", i), &testsuite.TestUpdateCallback{OnReject: func(error) { rejected++ }, OnComplete: func(any, error) { t.Error("invalid decision accepted") }}, bad)
					}
					if writes.Load() != 0 {
						t.Error("invalid decision dispatched write")
					}
					switch mode {
					case "cancel":
						env.UpdateWorkflowNoRejection(StopUpdate, "stop", t)
					case "timeout":
					default:
						cb := &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
							if err != nil || v != "recorded" {
								t.Errorf("receipt=%v %v", v, err)
							} else {
								completed++
								if good.Approved {
									authorized.Store(true)
								}
							}
						}}
						env.UpdateWorkflow(DecideUpdate, "valid", cb, good)
						env.UpdateWorkflow(DecideUpdate, "valid", cb, good) // Server/testsuite Update ID dedup.
						env.UpdateWorkflow(DecideUpdate, "duplicate", &testsuite.TestUpdateCallback{OnReject: func(error) { rejected++ }, OnComplete: func(any, error) { t.Error("different Update ID duplicate accepted") }}, good)
					}
				}, time.Second)
			}
			env.ExecuteWorkflow(wf)
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var result BatchResult
			if err := env.GetWorkflowResult(&result); err != nil {
				t.Fatal(err)
			}
			wantWrites := int32(0)
			if mode == "approve" || mode == "display failure" {
				wantWrites = 1
			}
			if writes.Load() != wantWrites {
				t.Errorf("writes=%d want %d", writes.Load(), wantWrites)
			}
			if mode != "preflight mismatch" {
				wantRejected, wantCompleted := 3, 0
				if mode != "cancel" && mode != "timeout" {
					wantRejected, wantCompleted = 4, 2
				}
				if rejected != wantRejected || completed != wantCompleted {
					t.Errorf("rejects=%d receipts=%d", rejected, completed)
				}
			}
			if mode == "cancel" && (!result.Results[0].IsError || resolved.Decision != "cancel") {
				t.Fatalf("cancel result=%+v resolved=%+v", result, resolved)
			}
			if mode == "timeout" && resolved.Decision != "timeout" {
				t.Fatalf("resolved=%+v", resolved)
			}
			if mode == "deny" && result.Results[0].Output != "denied by alice: unsafe" {
				t.Fatal(result)
			}
			if mode == "timeout" && result.Results[0].Output != "approval timed out" {
				t.Fatal(result)
			}
			if mode == "deny" && (resolved.Decision != "deny" || resolved.User != "alice" || resolved.Reason != "unsafe") {
				t.Fatalf("resolved=%+v", resolved)
			}
		})
	}
}

func TestApprovalDeadlineValidator(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		r := runtime{state: State{PendingApproval: &PendingApproval{RequestID: "request", ActionHash: "hash", ExpiresAt: workflow.Now(ctx)}}}
		d := DecisionInput{RequestID: "request", ActionHash: "hash", User: "alice"}
		if err := r.validateDecision(ctx, d); err == nil || !strings.Contains(err.Error(), "stale") {
			return fmt.Errorf("deadline accepted: %v", err)
		}
		if r.state.PendingApproval.Decision != nil {
			return fmt.Errorf("validator mutated state")
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}
