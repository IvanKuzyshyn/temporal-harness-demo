package workflows_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
)

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
		if len(messages) != 2 || messages[0].Role != "user" || messages[1].Content != state.Answer {
			t.Errorf("messages=%+v", messages)
		}
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
