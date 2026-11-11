package workflows_test

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"os"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
)

func assertSessionTranscript(t *testing.T, messages []llm.Message, prompt, answer string) {
	t.Helper()
	byRole := make(map[string][]llm.Message)
	for _, message := range messages {
		byRole[message.Role] = append(byRole[message.Role], message)
	}
	if len(messages) != 3 || len(byRole["system"]) != 1 || len(byRole["user"]) != 1 || len(byRole["assistant"]) != 1 {
		t.Fatalf("want one evidence snapshot, one user and one assistant: %+v", messages)
	}
	if byRole["user"][0].Content != prompt || byRole["assistant"][0].Content != answer {
		t.Errorf("messages=%+v", messages)
	}
}

type corruptingModel struct{ path string }

func (m corruptingModel) Chat(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
	if err := os.WriteFile(m.path, []byte("invalid complete record\n"), 0600); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Message: llm.Message{Content: "durable answer"}}, nil
}

func TestSessionSurvivesModelCompletionDisplayFailure(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "broken-display"})
	dir := t.TempDir()
	path, _ := events.NewStore(dir).Path("broken-display")
	env.RegisterActivity(&activities.Activities{Model: corruptingModel{path: path}})
	env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "input", t, w.MessageInput{Text: "hello"}) }, 0)
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		if s.Status != "idle" || s.Answer != "durable answer" || s.LastRunFailure != nil {
			t.Errorf("state=%+v", s)
		}
		messages, err := activities.ReadTranscript(dir, s.TranscriptRef)
		if err != nil {
			t.Fatal(err)
		}
		assertSessionTranscript(t, messages, "hello", s.Answer)
		env.CancelWorkflow()
	}, time.Minute)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{DataDir: dir})
}

func TestSessionWithRealMockActivities(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "session-persistence"})
	a := &activities.Activities{Model: mock.New()}
	env.RegisterActivity(a)
	dir := t.TempDir()
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflowNoRejection(w.MessageUpdate, "input", t, w.MessageInput{Text: "hello", RequestID: "input"})
	}, 0)
	env.RegisterDelayedCallback(func() {
		state := query(t, env)
		if state.Status != "idle" || state.LastRunFailure != nil || state.Answer != "Mock assistant: ready." {
			t.Errorf("state=%+v", state)
		}
		messages, err := activities.ReadTranscript(dir, state.TranscriptRef)
		if err != nil {
			t.Fatal(err)
		}
		assertSessionTranscript(t, messages, "hello", state.Answer)
		path, err := events.NewStore(dir).Path("session-persistence")
		if err != nil {
			t.Fatal(err)
		}
		stream, _, err := events.Tail(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(stream) < 7 {
			t.Fatalf("stream=%v", stream)
		}
		seen := map[events.Type]bool{}
		for _, event := range stream {
			seen[event.Type] = true
		}
		for _, typ := range []events.Type{events.AgentStart, events.TurnStart, events.MessageStart, events.MessageUpdate, events.MessageEnd, events.TurnEnd, events.AgentEnd, events.AgentSettled} {
			if !seen[typ] {
				t.Errorf("missing %s", typ)
			}
		}
		last := stream[len(stream)-1]
		var settled events.AgentData
		if err = json.Unmarshal(last.Data, &settled); err != nil {
			t.Fatal(err)
		}
		if last.Type != events.AgentSettled || settled.Status != "done" || last.Seq != state.LastSeq {
			t.Errorf("last=%+v state=%+v", last, state)
		}
		env.CancelWorkflow()
	}, time.Minute)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{Model: "mock", DataDir: dir})
}
