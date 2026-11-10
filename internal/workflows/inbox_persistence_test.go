package workflows_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// Real LLMStream persists the assistant and loads the next request from disk.
// Capture actual model messages, not workflow AppendTranscript start arguments.
type inboxRecordingModel struct {
	mu       sync.Mutex
	requests [][]llm.Message
	tool     bool
}

func (m *inboxRecordingModel) Chat(_ context.Context, req llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, append([]llm.Message(nil), req.Messages...))
	if m.tool && len(m.requests) == 1 {
		return llm.Response{Message: llm.Message{ToolCalls: []llm.ToolCall{{ID: "read", Name: "read_file", Args: []byte(`{"path":"x"}`)}}}}, nil
	}
	return llm.Response{Message: llm.Message{Content: "final answer"}}, nil
}

func (m *inboxRecordingModel) messages() [][]llm.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]llm.Message(nil), m.requests...)
}

// Provider policy and quoted snapshots are request-only; compare the complete
// conversational spine to prove assistant/tool pairing and retained input.
func conversationMessages(messages []llm.Message) []llm.Message {
	var out []llm.Message
	for _, m := range messages {
		if m.Role == "system" || strings.HasPrefix(m.Content, "Supplemental project DATA") {
			continue
		}
		out = append(out, m)
	}
	return out
}

func requireInboxCanceled(t *testing.T, env *testsuite.TestWorkflowEnvironment, checkpoints ...bool) {
	t.Helper()
	for i, reached := range checkpoints {
		if !reached {
			t.Errorf("checkpoint %d skipped", i)
		}
	}
	if !env.IsWorkflowCompleted() || !temporal.IsCanceledError(env.GetWorkflowError()) {
		t.Errorf("expected explicit cancellation after checkpoints, got %v", env.GetWorkflowError())
	}
}

func TestInboxStopToolsContinueRealPersistence(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovered=%v", recovered), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "x"), []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			model := &inboxRecordingModel{tool: true}
			a := &activities.Activities{Model: model}
			env.RegisterActivity(a)
			if recovered {
				// Exercise the joined interrupted/recovered outcome contract. The
				// approval/unsafe executor is independently owned (stub here).
				env.OnActivity(w.ToolActivity, mock.Anything, mock.Anything).Return(tools.Result{ToolCallID: "read", Output: "recovered partial output", IsError: true, Interrupted: true}, nil)
			} else {
				env.OnActivity(w.ToolActivity, mock.Anything, mock.Anything).After(time.Hour).Return(tools.Result{Output: "stale"}, nil)
			}
			// Delay only result persistence, then execute the REAL append. This
			// also proves Stop during persistence cannot cancel its context.
			env.OnActivity(w.TranscriptActivity, mock.Anything, mock.MatchedBy(func(in w.AppendInput) bool { return in.Messages[0].Role == "tool" })).After(time.Second).Return(a.AppendTranscript)
			env.OnActivity(w.TranscriptActivity, mock.Anything, mock.MatchedBy(func(in w.AppendInput) bool { return in.Messages[0].Role != "tool" })).Return(a.AppendTranscript)
			started, stopped, checked, settled := false, false, false, false
			toolCalls := 0
			var resultTail []llm.Message
			var durableAtStop []llm.Message
			env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
				switch info.ActivityType.Name {
				case w.ToolActivity:
					started = true
					toolCalls++
					if !recovered {
						env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.StopUpdate, "stop", t) }, time.Millisecond)
					}
				case w.TranscriptActivity:
					var in w.AppendInput
					if err := args.Get(&in); err != nil {
						t.Fatal(err)
					}
					if recovered && in.Messages[0].Role == "tool" {
						env.UpdateWorkflowNoRejection(w.StopUpdate, "stop", t)
					}
				case w.EmitActivity:
					var in w.EmitInput
					if err := args.Get(&in); err != nil {
						t.Fatal(err)
					}
					if in.Type == events.AgentEnd && !settled {
						settled = true
						var err error
						durableAtStop, err = activities.ReadTranscript(dir, query(t, env).TranscriptRef)
						if err != nil {
							t.Fatal(err)
						}
						for _, msg := range durableAtStop {
							if msg.Role == "tool" {
								resultTail = append(resultTail, msg)
							}
						}
						if len(resultTail) != 1 || resultTail[0].ToolCallID != "read" {
							t.Errorf("settled before durable result: %+v", durableAtStop)
						}
					}
				}
			})
			env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "input", t, w.MessageInput{Text: "read x"}) }, 0)
			env.RegisterDelayedCallback(func() {
				stopped = true
				s := query(t, env)
				if s.Status != "idle" || s.LastRunFailure == nil || s.LastRunFailure.Reason != "aborted" || !s.HasUnansweredContext || len(s.ActiveTools) != 0 {
					t.Errorf("not stopped: %+v", s)
				}
				env.UpdateWorkflowNoRejection(w.ContinueUpdate, "continue", t)
			}, 3*time.Second)
			env.RegisterDelayedCallback(func() {
				checked = true
				modelMessages := model.messages()
				if len(modelMessages) != 2 || toolCalls != 1 {
					t.Errorf("model calls=%d tool calls=%d", len(modelMessages), toolCalls)
				}
				if len(modelMessages) == 2 && !reflect.DeepEqual(conversationMessages(modelMessages[1]), conversationMessages(durableAtStop)) {
					t.Errorf("resumed request differs from finalized transcript: %+v; durable=%+v", modelMessages[1], durableAtStop)
				}
				if len(resultTail) == 1 {
					want := "canceled"
					if recovered {
						want = "[interrupted]\nrecovered partial output"
					}
					if !strings.Contains(resultTail[0].Content, want) {
						t.Errorf("lost interruption/recovery: %+v", resultTail[0])
					}
				}
				s := query(t, env)
				messages, err := activities.ReadTranscript(dir, s.TranscriptRef)
				if err != nil {
					t.Fatal(err)
				}
				if len(messages) != len(durableAtStop)+2 || messages[len(messages)-1].Content != "final answer" || s.HasUnansweredContext || s.LastRunFailure != nil {
					t.Errorf("resumed transcript/state: %+v %+v", messages, s)
				}
				env.CancelWorkflow()
			}, 5*time.Second)
			env.ExecuteWorkflow(w.SessionWorkflow, w.Input{DataDir: dir, Workdir: dir})
			requireInboxCanceled(t, env, started, settled, stopped, checked)
		})
	}
}

func TestInboxToolPersistenceFailureFailsClosed(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	model := &inboxRecordingModel{tool: true}
	a := &activities.Activities{Model: model}
	env.RegisterActivity(a)
	env.OnActivity(w.TranscriptActivity, mock.Anything, mock.MatchedBy(func(in w.AppendInput) bool { return in.Messages[0].Role == "tool" })).Return(w.AppendResult{}, temporal.NewApplicationError("store unavailable", "StoreUnavailable")).Times(3)
	env.OnActivity(w.TranscriptActivity, mock.Anything, mock.MatchedBy(func(in w.AppendInput) bool { return in.Messages[0].Role != "tool" })).Return(a.AppendTranscript)
	stopped, settled, toolCalls := false, false, 0
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		switch info.ActivityType.Name {
		case w.ToolActivity:
			toolCalls++
		case w.TranscriptActivity:
			var in w.AppendInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			if in.Messages[0].Role == "tool" && !stopped {
				if info.ScheduleToCloseTimeout != 3*time.Minute || info.StartToCloseTimeout != time.Minute {
					t.Errorf("unbounded result finalization options: %+v", info)
				}
				stopped = true
				env.UpdateWorkflowNoRejection(w.StopUpdate, "stop", t)
			}
		case w.EmitActivity:
			var in w.EmitInput
			if err := args.Get(&in); err != nil {
				t.Fatal(err)
			}
			if in.Type == events.AgentEnd || in.Type == events.AgentSettled {
				settled = true
			}
		}
	})
	env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "input", t, w.MessageInput{Text: "read x"}) }, 0)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{DataDir: dir, Workdir: dir})
	var appErr *temporal.ApplicationError
	if !errors.As(env.GetWorkflowError(), &appErr) || appErr.Type() != "ToolResultPersistence" || !appErr.NonRetryable() {
		t.Errorf("expected nonretryable finalization failure, got %v", env.GetWorkflowError())
	}
	if !stopped || settled || len(model.messages()) != 1 || toolCalls != 1 {
		t.Errorf("unsafe boundary: stopped=%v settled=%v models=%d tools=%d", stopped, settled, len(model.messages()), toolCalls)
	}
	s := query(t, env)
	if !env.IsWorkflowCompleted() || s.Status != "failed" || s.LastRunFailure == nil || s.LastRunFailure.Reason != "interrupted" || !s.QueuePaused || len(s.ActiveTools) != 0 || s.PendingApproval != nil {
		t.Errorf("closed workflow must project terminal persistence failure: %+v", s)
	}
	if s.LastRunFailure != nil && !strings.Contains(s.LastRunFailure.Detail, "ToolResultPersistence") {
		t.Errorf("lost fatal persistence detail: %+v", s.LastRunFailure)
	}
	env.AssertExpectations(t)
}

func TestInboxFinalSteerBudgetContinueRealPersistence(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	dir := t.TempDir()
	model := &inboxRecordingModel{}
	env.RegisterActivity(&activities.Activities{Model: model})
	steered, resumed, checked := false, false, false
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, args converter.EncodedValues) {
		if info.ActivityType.Name != w.EmitActivity {
			return
		}
		var in w.EmitInput
		if err := args.Get(&in); err != nil {
			t.Fatal(err)
		}
		if in.Type == events.TurnEnd && !steered {
			steered = true
			env.UpdateWorkflowNoRejection(w.MessageUpdate, "steer", t, w.MessageInput{Text: "retained steer", WhenBusy: "steer"})
		}
	})
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflowNoRejection(w.MessageUpdate, "first", t, w.MessageInput{Text: "first question"})
	}, 0)
	env.RegisterDelayedCallback(func() {
		resumed = true
		s := query(t, env)
		if s.HasUnansweredContext || !s.QueuePaused || len(s.Inbox) != 1 || s.LastRunFailure == nil || s.LastRunFailure.Reason != "turn_limit" {
			t.Errorf("budget state=%+v", s)
		}
		env.UpdateWorkflowNoRejection(w.ContinueUpdate, "resume", t)
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		checked = true
		modelMessages := model.messages()
		if len(modelMessages) != 2 {
			t.Errorf("calls=%d, wanted exactly two", len(modelMessages))
		}
		if len(modelMessages) >= 2 {
			messages := conversationMessages(modelMessages[1])
			last := messages[len(messages)-1]
			if last.Role != "user" || last.Content != "retained steer" {
				t.Errorf("empty/context-only resumed call: %+v", messages)
			}
			if messages[len(messages)-2].Role != "assistant" || messages[len(messages)-2].Content != "final answer" {
				t.Errorf("missing prior durable answer: %+v", messages)
			}
		}
		s := query(t, env)
		messages, err := activities.ReadTranscript(dir, s.TranscriptRef)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 6 || s.RunCounter != 2 || s.HasUnansweredContext || s.LastRunFailure != nil {
			t.Errorf("final transcript/state: %+v %+v", messages, s)
		}
		env.CancelWorkflow()
	}, 2*time.Second)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{DataDir: dir, Workdir: dir, MaxTurns: 1})
	requireInboxCanceled(t, env, steered, resumed, checked)
}
