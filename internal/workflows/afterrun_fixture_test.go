package workflows_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// Scripted model only: this test validates real writes, go-test processes,
// artifact persistence and next-request hydration, NOT real-model behavior.
type afterRunFixtureModel struct {
	mu       sync.Mutex
	requests [][]llm.Message
}

func (m *afterRunFixtureModel) Chat(_ context.Context, req llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, append([]llm.Message(nil), req.Messages...))
	n := len(m.requests)
	if n == 1 || n == 3 {
		value := "false"
		if n == 3 {
			value = "true"
		}
		content := "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if !" + value + " { t.Fatal(\"fixture assertion failed\") } }\n"
		args, _ := json.Marshal(map[string]string{"path": "value_test.go", "content": content})
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "write", Name: "write_file", Args: args}}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "Ready for verification."}}, nil
}
func TestAfterRunRealGoFixtureAndTranscript(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("GOWORK", "off")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module afterrunfixture\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Minute)
	model := &afterRunFixtureModel{}
	a := &activities.Activities{Model: model}
	env.RegisterActivity(a)
	commands, approvals, ends, settled := 0, 0, 0, 0
	checked := false
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		switch info.ActivityType.Name {
		case w.NotifyActivity:
			p := query(t, env).PendingApproval
			if p == nil {
				t.Fatal("missing real-action approval")
			}
			approvals++
			env.UpdateWorkflowNoRejection(w.DecideUpdate, p.RequestID, t, w.DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: true, User: "fixture-human"})
		case w.CommandActivity:
			commands++
			if info.Attempt != 1 || approvals != commands*2 {
				t.Errorf("unapproved/retried process: commands=%d approvals=%d attempt=%d", commands, approvals, info.Attempt)
			}
		case w.EmitActivity:
			var in w.EmitInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			if in.Type == events.AgentEnd {
				ends++
			}
			if in.Type == events.AgentSettled {
				settled++
				checked = true
				s := query(t, env)
				if commands != 2 || approvals != 4 || ends != 2 || settled != 1 || s.AfterRunCount != 1 || s.VerificationPending || s.HasUnansweredContext || s.LastRunFailure != nil || !strings.Contains(s.Answer, "go test ./... passed") {
					t.Errorf("real chain: %+v commands=%d approvals=%d ends=%d", s, commands, approvals, ends)
				}
				messages, err := activities.ReadTranscript(data, s.TranscriptRef)
				if err != nil {
					t.Fatal(err)
				}
				verify := func(messages []llm.Message) int {
					count := 0
					for i, msg := range messages {
						if msg.Role != "tool" || msg.ToolName != "run_command" {
							continue
						}
						if i == 0 || messages[i-1].Role != "assistant" || len(messages[i-1].ToolCalls) != 1 || messages[i-1].ToolCalls[0].ID != msg.ToolCallID {
							t.Fatalf("unpaired synthetic result: %+v", messages)
						}
						var out struct {
							ExitCode    *int   `json:"exitCode"`
							IsError     bool   `json:"isError"`
							Interrupted bool   `json:"interrupted"`
							LogRef      string `json:"logRef"`
							Tail        string `json:"tail"`
						}
						if err := json.Unmarshal([]byte(msg.Content), &out); err != nil {
							t.Fatal(err)
						}
						if out.ExitCode == nil || out.LogRef == "" || out.Interrupted {
							t.Fatalf("missing real outcome: %s", msg.Content)
						}
						log, err := os.ReadFile(filepath.Join(data, out.LogRef))
						if err != nil {
							t.Fatal(err)
						}
						if count == 0 {
							if *out.ExitCode <= 0 || !out.IsError || !strings.Contains(string(log), "fixture assertion failed") || !strings.Contains(out.Tail, "fixture assertion failed") {
								t.Errorf("expected actual failing test: %s log=%s", msg.Content, log)
							}
						} else if *out.ExitCode != 0 || out.IsError || !strings.Contains(string(log), "ok") {
							t.Errorf("expected passing test: %s log=%s", msg.Content, log)
						}
						count++
					}
					return count
				}
				if n := verify(messages); n != 2 {
					t.Errorf("durable verifications=%d", n)
				}
				model.mu.Lock()
				requests := append([][]llm.Message(nil), model.requests...)
				model.mu.Unlock()
				if len(requests) != 4 {
					t.Errorf("model calls=%d", len(requests))
				} else if n := verify(requests[2]); n != 1 {
					t.Errorf("repair model missing hydrated failed result: %d", n)
				}
				env.RegisterDelayedCallback(env.CancelWorkflow, time.Millisecond)
			}
		}
	})
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflowNoRejection(w.MessageUpdate, "original", t, w.MessageInput{Text: "write the test", RequestID: "original"})
	}, 0)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{Workdir: root, DataDir: data, HangTimeout: 30 * time.Second})
	if !checked || !temporal.IsCanceledError(env.GetWorkflowError()) {
		t.Fatalf("fixture chain did not finish: %v", env.GetWorkflowError())
	}
}
