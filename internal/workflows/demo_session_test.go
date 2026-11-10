package workflows_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// This is a fake provider, NOT real-model validation. It supplies streaming
// commentary and unsafe/invalid calls to exercise the same activity boundary.
type demoCommentaryProvider struct{}

func (demoCommentaryProvider) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if err := emit(llm.Delta{Text: "Unverified provider commentary 界", Tokens: 4}); err != nil {
		return llm.Response{}, err
	}
	m := llm.Message{Content: "Provider claims everything passed (not evidence)."}
	if req.Scenario == "demo" {
		m.ToolCalls = []llm.ToolCall{{Name: "run_command", Args: json.RawMessage(`{"cmd":"rm","args":["-rf","/"]}`)}}
	}
	return llm.Response{Message: m, Tokens: 4}, nil
}

func TestDemoSessionActualActivities(t *testing.T) {
	for _, mode := range []string{"mock", "fake-provider", "deny-write", "deny-command", "failing-tests", "unrecognized"} {
		t.Run(mode, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			for _, name := range []string{"go.mod", "calc/calc.go", "calc/calc_test.go"} {
				content, err := os.ReadFile(filepath.Join("../../examples/sample-project", name))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "failing-tests" {
				if err := os.WriteFile(filepath.Join(root, "calc/fail_test.go"), []byte("package calc\nimport \"testing\"\nfunc TestFailure(t *testing.T){t.Fatal(\"intentional additional failure\")}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unrecognized" {
				if err := os.WriteFile(filepath.Join(root, "calc/calc.go"), []byte("package calc\n// custom code must survive\nfunc Add(a,b int)int{return a-b}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var provider llm.Model = demoCommentaryProvider{}
			if mode == "mock" {
				provider = mock.New()
			}
			a := &activities.Activities{Model: provider}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetTestTimeout(90 * time.Second)
			env.RegisterWorkflow(w.ExplorerWorkflow)
			env.RegisterActivityWithOptions(a.LLMStream, activity.RegisterOptions{Name: w.LLMActivity})
			env.RegisterActivityWithOptions(a.AppendTranscript, activity.RegisterOptions{Name: w.TranscriptActivity})
			env.RegisterActivityWithOptions(a.GatherContext, activity.RegisterOptions{Name: w.GatherActivity})
			env.RegisterActivityWithOptions(a.PreflightTool, activity.RegisterOptions{Name: w.PreflightActivity})
			env.RegisterActivityWithOptions(a.ExecuteTool, activity.RegisterOptions{Name: w.ToolActivity})
			env.RegisterActivityWithOptions(a.RunCommand, activity.RegisterOptions{Name: w.CommandActivity})
			env.RegisterActivityWithOptions(a.RecoverCommand, activity.RegisterOptions{Name: w.RecoverCommandActivity})
			env.RegisterActivityWithOptions(a.EmitEvent, activity.RegisterOptions{Name: w.EmitActivity})
			// No desktop notification side effects in tests; approval still uses explicit
			// real Decide Updates with the authoritative query's exact request/hash.
			env.RegisterActivityWithOptions(func(ctx context.Context, in w.EmitInput) error { _, err := a.EmitEvent(ctx, in); return err }, activity.RegisterOptions{Name: w.NotifyActivity})
			phase := 0
			reads, parallel, childModels, demoModels, decisions, commands := 0, 0, 0, 0, 0, 0
			finished := false
			repeat := mode == "mock" || mode == "fake-provider"
			env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
				switch info.ActivityType.Name {
				case w.LLMActivity:
					var in w.LLMInput
					if err := args.Get(&in); err != nil {
						t.Fatal(err)
					}
					if in.Scope.ParentWorkflowID != "" {
						childModels++
					} else if in.Scenario == "demo" {
						demoModels++
					}
				case w.ToolActivity, w.CommandActivity:
					var in w.ToolInput
					if err := args.Get(&in); err != nil {
						t.Fatal(err)
					}
					if in.Scope.ParentWorkflowID == "" && in.Call.Name == "read_file" {
						reads++
					}
					if in.Call.Name == "run_command" {
						commands++
						if string(in.Call.Args) != `{"cmd":"go","args":["test","./..."]}` {
							t.Errorf("unsafe command: %s", in.Call.Args)
						}
					}
				case w.NotifyActivity:
					env.RegisterDelayedCallback(func() {
						p := query(t, env).PendingApproval
						if p == nil {
							t.Fatal("missing approval")
						}
						approved := !(mode == "deny-write" && p.Call.Name == "write_file") && !(mode == "deny-command" && p.Call.Name == "run_command")
						decisions++
						env.UpdateWorkflowNoRejection(w.DecideUpdate, fmt.Sprintf("decision-%d", decisions), t, w.DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: approved, User: "demo-test", Reason: mode})
					}, 0)
				case w.EmitActivity:
					var in w.EmitInput
					if err := args.Get(&in); err != nil {
						t.Fatal(err)
					}
					if in.Scope.ParentWorkflowID != "" {
						return
					}
					if in.Type == events.ParallelStart {
						raw, _ := json.Marshal(in.Data)
						var p events.ParallelData
						_ = json.Unmarshal(raw, &p)
						if len(p.ToolCallIDs) == 2 && p.Concurrency > 1 {
							parallel++
						}
					}
					if in.Type != events.AgentSettled {
						return
					}
					env.RegisterDelayedCallback(func() {
						s := query(t, env)
						wantFailure := ""
						if phase == 1 && mode == "deny-command" {
							wantFailure = "interrupted"
						} else if phase == 1 && mode == "failing-tests" {
							wantFailure = "restart_limit"
						}
						gotFailure := ""
						if s.LastRunFailure != nil {
							gotFailure = s.LastRunFailure.Reason
						}
						if gotFailure != wantFailure {
							t.Fatalf("failure=%+v, want %q", s.LastRunFailure, wantFailure)
						}
						if wantFailure != "" && (!s.QueuePaused || !s.VerificationPending || !s.HasUnansweredContext) {
							t.Fatalf("verification failure lost resumability: %+v", s)
						}
						if phase == 1 && mode == "failing-tests" && (s.AfterRunCount != 2 || !strings.Contains(s.Answer, "Verification: go test ./... failed")) {
							t.Fatalf("missing bounded repair outcome: %+v", s)
						}
						if phase == 0 { // Demo starts in an already-open ordinary-chat session.
							phase = 1
							env.UpdateWorkflowNoRejection(w.MessageUpdate, "demo-one", t, w.MessageInput{Text: "/demo"})
							return
						}
						if phase == 1 || phase == 2 {
							passed := mode == "mock" || mode == "fake-provider"
							if strings.Contains(s.Answer, "tests passed") != passed {
								t.Errorf("untruthful tests: %s", s.Answer)
							}
							wrote := mode != "deny-write" && mode != "unrecognized"
							if mode != "failing-tests" && strings.Contains(s.Answer, "write applied") != wrote {
								t.Errorf("untruthful write: %s", s.Answer)
							}
							if mode == "unrecognized" && !strings.Contains(s.Answer, "not the exact recognized teaching fixture") {
								t.Error(s.Answer)
							}
							if repeat && phase == 1 {
								phase = 2
								env.UpdateWorkflowNoRejection(w.MessageUpdate, "demo-two", t, w.MessageInput{Text: "/demo"})
								return
							}
							phase = 3
							env.UpdateWorkflowNoRejection(w.MessageUpdate, "normal-after", t, w.MessageInput{Text: "ordinary chat"})
							return
						}
						if strings.Contains(s.Answer, "Forced demo plan") {
							t.Error("later normal chat repeated demo")
						}
						count := 1
						if repeat {
							count = 2
						}
						wantTurns := 5 * count
						if mode == "unrecognized" {
							wantTurns = 3
						}
						// Every write earns a separate approval-gated afterRun check.
						wantDecisions, wantCommands, wantHooks := 3*count, 2*count, count
						switch mode {
						case "unrecognized":
							wantDecisions, wantCommands, wantHooks = 0, 0, 0
						case "deny-write":
							wantDecisions, wantCommands, wantHooks = 2, 1, 0
						case "deny-command":
							wantDecisions, wantCommands, wantHooks = 3, 0, 1
						case "failing-tests":
							// Scripted command, first hook, then two no-write repairs.
							wantDecisions, wantCommands, wantHooks = 5, 4, 3
						}
						if reads != 2*count || parallel != count || childModels < count || len(s.Children) != count || demoModels != wantTurns || decisions != wantDecisions || commands != wantCommands {
							t.Errorf("patterns reads=%d parallel=%d childModels=%d children=%d modelTurns=%d approvals=%d commands=%d", reads, parallel, childModels, len(s.Children), demoModels, decisions, commands)
						}
						content, err := os.ReadFile(filepath.Join(root, "calc/calc.go"))
						if err != nil {
							t.Fatal(err)
						}
						if mode == "unrecognized" && !strings.Contains(string(content), "custom code must survive") {
							t.Fatal("overwrote unrelated file")
						}
						if mode == "deny-write" && !strings.Contains(string(content), "return a - b") {
							t.Fatal("denied write executed")
						}
						if mode != "deny-write" && mode != "unrecognized" && !strings.Contains(string(content), "return a + b") {
							t.Fatal("approved fixture correction missing")
						}
						messages, err := activities.ReadTranscript(data, s.TranscriptRef)
						if err != nil {
							t.Fatal(err)
						}
						pending := map[string]string{}
						hooks := 0
						for _, m := range messages {
							if m.Role == "assistant" {
								for _, call := range m.ToolCalls {
									if _, ok := pending[call.ID]; ok {
										t.Fatal("duplicate ID")
									}
									pending[call.ID] = call.Name
								}
							}
							if m.Role == "tool" {
								if pending[m.ToolCallID] != m.ToolName {
									t.Fatalf("unpaired tool: %+v", m)
								}
								delete(pending, m.ToolCallID)
								if strings.HasSuffix(m.ToolCallID, "/afterrun/tests") {
									hooks++
									var result struct {
										ExitCode       *int   `json:"exitCode"`
										LogRef         string `json:"logRef"`
										IsError        bool   `json:"isError"`
										Interrupted    bool   `json:"interrupted"`
										ExecutionError bool   `json:"executionError"`
									}
									if err := json.Unmarshal([]byte(m.Content), &result); err != nil {
										t.Fatal(err)
									}
									if result.Interrupted || result.ExecutionError {
										t.Fatalf("unexpected synthetic infrastructure failure: %+v", result)
									}
									if mode == "deny-command" {
										if result.ExitCode != nil || result.LogRef != "" || !result.IsError {
											t.Fatalf("denied synthetic test reported execution: %+v", result)
										}
									} else if result.ExitCode == nil || result.LogRef == "" || (mode == "failing-tests" && (*result.ExitCode <= 0 || !result.IsError)) || (repeat && (*result.ExitCode != 0 || result.IsError)) {
										t.Fatalf("untruthful synthetic test result: %+v", result)
									}
								} else if m.ToolName == "run_command" {
									var result tools.Result
									if err := json.Unmarshal([]byte(m.Content), &result); err != nil {
										t.Fatal(err)
									}
									if mode != "deny-command" && (result.Details["exitCode"] == "" || result.Details["logRef"] == "") {
										t.Fatal("lost command metadata")
									}
								}
							}
						}
						if hooks != wantHooks {
							t.Fatalf("synthetic pairs=%d, want %d", hooks, wantHooks)
						}
						if len(pending) != 0 {
							t.Fatalf("unresolved tool calls: %v", pending)
						}
						finished = true
						env.CancelWorkflow()
					}, time.Millisecond)
				}
			})
			env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "hello", t, w.MessageInput{Text: "hello"}) }, 0)
			env.ExecuteWorkflow(w.SessionWorkflow, w.Input{Workdir: root, DataDir: data, Model: "fake-model", ApprovalTimeout: time.Minute})
			if !finished || !temporal.IsCanceledError(env.GetWorkflowError()) {
				t.Fatalf("unfinished=%v error=%v", !finished, env.GetWorkflowError())
			}
		})
	}
}
