package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type cliFake struct {
	states      []workflows.State
	queries     int
	submitted   workflows.MessageInput
	input       workflows.Input
	decisions   []workflows.DecisionInput
	submit      func()
	queryErrors map[int]error
	listError   error
	disposition string
	decideError error
}

func (f *cliFake) Query(context.Context, string) (workflows.State, error) {
	i := f.queries
	f.queries++
	if err := f.queryErrors[i]; err != nil {
		return workflows.State{}, err
	}
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	return f.states[i], nil
}
func (f *cliFake) Submit(_ context.Context, _ string, in workflows.Input, m workflows.MessageInput) (workflows.MessageReceipt, error) {
	f.input = in
	f.submitted = m
	if f.submit != nil {
		f.submit()
	}
	disposition := f.disposition
	if disposition == "" {
		disposition = "started"
	}
	return workflows.MessageReceipt{Disposition: disposition}, nil
}
func (f *cliFake) Decide(_ context.Context, _ string, d workflows.DecisionInput) error {
	f.decisions = append(f.decisions, d)
	return f.decideError
}
func (f *cliFake) Sessions(context.Context) ([]string, error) { return []string{"s"}, f.listError }

func TestRunHistoricalAndChildSettlement(t *testing.T) {
	dir := t.TempDir()
	store := events.NewStore(dir)
	scope := events.Scope{StreamID: "s", WorkflowID: "s"}
	if _, err := store.Append(scope, events.AgentSettled, events.None, events.AgentData{}); err != nil {
		t.Fatal(err)
	}
	f := &cliFake{states: []workflows.State{{Status: "idle", RunCounter: 2}, {Status: "idle", RunCounter: 2}, {Status: "working", RunCounter: 3}, {Status: "idle", RunCounter: 3, Answer: "authoritative"}}}
	f.submit = func() {
		scope.WorkflowID = "child"
		scope.ParentWorkflowID = "s"
		_, err := store.Append(scope, events.AgentSettled, events.Child, events.AgentData{})
		if err != nil {
			t.Fatal(err)
		}
	}
	var out, diag bytes.Buffer
	err := runSession(context.Background(), f, config.Config{DataDir: dir}, runOptions{session: "s", prompt: "hello", json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var final cliSummary
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &final); err != nil {
		t.Fatalf("expected only final JSON, no historical/child terminal records: %q: %v", out.String(), err)
	}
	if f.queries != 4 || final.Type != "session_state" || final.Answer != "authoritative" || final.ID != "s" {
		t.Fatalf("queries=%d output=%q", f.queries, out.String())
	}
	if len(f.decisions) != 0 || diag.Len() != 0 || f.submitted.RequestID == "" {
		t.Fatalf("unexpected defaults: %+v %s", f, diag.String())
	}
}
func TestRunFailureAndJSON(t *testing.T) {
	f := &cliFake{states: []workflows.State{{Status: "idle"}, {Status: "idle", RunCounter: 1, LastRunFailure: &workflows.Failure{Reason: "broken"}}}}
	var out, diag bytes.Buffer
	err := runSession(context.Background(), f, config.Config{DataDir: t.TempDir()}, runOptions{session: "s", prompt: "hello", json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
	if err == nil {
		t.Fatal("failure reported success")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"type":"session_state"`)) {
		t.Fatal(out.String())
	}
}
func TestWaitTimeoutAndBlocked(t *testing.T) {
	f := &cliFake{states: []workflows.State{{Status: "idle"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := pollSession(ctx, f, "s", "settled", time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	f.states = []workflows.State{{Status: "working", PendingApproval: &workflows.PendingApproval{RequestID: "r"}}}
	if _, err := pollSession(context.Background(), f, "s", "blocked", time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
func TestTerminalParentFilter(t *testing.T) {
	for _, e := range []events.Event{{WorkflowID: "child", ParentWorkflowID: "s", Type: events.AgentSettled}, {WorkflowID: "s", ParentWorkflowID: "parent", Type: events.AgentSettled}, {WorkflowID: "other", Type: events.AgentSettled}} {
		if terminalParent(e, "s") {
			t.Fatal(e)
		}
	}
	if !terminalParent(events.Event{WorkflowID: "s", Type: events.AgentSettled}, "s") {
		t.Fatal("parent not recognized")
	}
}
