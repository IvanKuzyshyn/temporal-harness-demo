package workflows_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// Cross the real activity/filesystem boundary as well as spying on dispatch.
// Neither invalid decisions nor denial/timeout/stop may touch the target file.
func TestApprovalSessionRealWrite(t *testing.T) {
	for _, mode := range []string{"approve", "deny", "timeout", "stop"} {
		t.Run(mode, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			root := t.TempDir()
			path := filepath.Join(root, "target.txt")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			a := &activities.Activities{}
			env.RegisterActivityWithOptions(a.PreflightTool, activity.RegisterOptions{Name: w.PreflightActivity})
			var dispatched atomic.Int32
			var authorized atomic.Bool
			env.RegisterActivityWithOptions(func(ctx context.Context, in w.ToolInput) (tools.Result, error) {
				if !authorized.Load() {
					t.Error("real ExecuteTool invoked without valid decision")
				}
				dispatched.Add(1)
				return a.ExecuteTool(ctx, in)
			}, activity.RegisterOptions{Name: w.ToolActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.EmitInput) (uint64, error) { return 0, nil }, activity.RegisterOptions{Name: w.EmitActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.EmitInput) error { return nil }, activity.RegisterOptions{Name: w.NotifyActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.GatherInput) (w.AppendResult, error) {
				return w.AppendResult{TranscriptRef: "ref"}, nil
			}, activity.RegisterOptions{Name: w.GatherActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.AppendInput) (w.AppendResult, error) {
				return w.AppendResult{TranscriptRef: "ref"}, nil
			}, activity.RegisterOptions{Name: w.TranscriptActivity})
			env.RegisterActivityWithOptions(func(_ context.Context, in w.LLMInput) (w.LLMResult, error) {
				if in.Turn == 0 {
					return w.LLMResult{ToolCalls: []llm.ToolCall{{ID: "write", Name: "write_file", Args: []byte(`{"path":"target.txt","content":"approved content"}`)}}}, nil
				}
				return w.LLMResult{Summary: "done"}, nil
			}, activity.RegisterOptions{Name: w.LLMActivity})
			assertFile := func(want string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil || string(data) != want {
					t.Errorf("file=%q err=%v want=%q", data, err, want)
				}
			}
			env.RegisterDelayedCallback(func() {
				env.UpdateWorkflowNoRejection(w.MessageUpdate, "message", t, w.MessageInput{Text: "change the file"})
			}, 0)
			env.RegisterDelayedCallback(func() {
				assertFile("original")
				if dispatched.Load() != 0 {
					t.Fatal("write dispatched while blocked")
				}
				p := query(t, env).PendingApproval
				if p == nil {
					t.Fatal("missing pending request")
				}
				env.UpdateWorkflow(w.DecideUpdate, "wrong-hash", &testsuite.TestUpdateCallback{OnReject: func(error) {}, OnComplete: func(any, error) { t.Error("hash mismatch accepted") }}, w.DecisionInput{RequestID: p.RequestID, ActionHash: "wrong", Approved: true, User: "alice"})
			}, time.Second)
			env.RegisterDelayedCallback(func() {
				assertFile("original")
				if dispatched.Load() != 0 {
					t.Fatal("invalid hash released real write")
				}
				p := query(t, env).PendingApproval
				if p == nil {
					t.Fatal("missing pending request")
				}
				switch mode {
				case "approve", "deny":
					env.UpdateWorkflow(w.DecideUpdate, "valid", &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(_ any, err error) {
						if err != nil {
							t.Error(err)
						} else if mode == "approve" {
							authorized.Store(true)
						}
					}}, w.DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: mode == "approve", User: "alice", Reason: "unsafe"})
				case "stop":
					env.UpdateWorkflowNoRejection(w.StopUpdate, "stop", t)
				}
			}, 2*time.Second)
			env.RegisterDelayedCallback(func() {
				want := "original"
				count := int32(0)
				if mode == "approve" {
					want = "approved content"
					count = 1
				}
				assertFile(want)
				if dispatched.Load() != count {
					t.Errorf("dispatch=%d want=%d", dispatched.Load(), count)
				}
				if state := query(t, env); state.PendingApproval != nil || state.Status == "blocked" {
					t.Errorf("approval leaked: %+v", state)
				}
				env.CancelWorkflow()
			}, 5*time.Second)
			env.ExecuteWorkflow(w.SessionWorkflow, w.Input{Workdir: root, DataDir: t.TempDir(), ApprovalTimeout: 3 * time.Second})
		})
	}
}
