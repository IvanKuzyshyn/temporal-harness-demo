package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestRunInboxLifecycle(t *testing.T) {
	own := workflows.MessageInput{RequestID: "own", WhenBusy: "followUp"}
	steer := workflows.MessageInput{RequestID: "steer", WhenBusy: "steer"}
	follow := workflows.MessageInput{RequestID: "follow", WhenBusy: "followUp"}
	failure := &workflows.Failure{Reason: "model_error", Detail: "offline"}
	for _, tc := range []struct {
		name   string
		states []workflows.State
		failed bool
	}{
		{"queued steer consumes no run", []workflows.State{
			{Status: "working", RunCounter: 4, Inbox: []workflows.MessageInput{steer}},
			{Status: "working", RunCounter: 4, Inbox: []workflows.MessageInput{steer, own}},
			{Status: "working", RunCounter: 4, Inbox: []workflows.MessageInput{own}},
			{Status: "working", RunCounter: 5},
			{Status: "idle", RunCounter: 5, Answer: "new answer"},
		}, false},
		{"steer and older followup drain before own", []workflows.State{
			{Status: "working", RunCounter: 4, Inbox: []workflows.MessageInput{steer, follow}},
			{Status: "working", RunCounter: 4, Inbox: []workflows.MessageInput{follow, own}},
			{Status: "working", RunCounter: 5, Inbox: []workflows.MessageInput{own}},
			{Status: "idle", RunCounter: 5, Inbox: []workflows.MessageInput{own}, Answer: "older answer"},
			{Status: "working", RunCounter: 6},
			{Status: "idle", RunCounter: 6, Answer: "new answer"},
		}, false},
		{"old idle settlement cannot finish submit", []workflows.State{
			{Status: "idle", RunCounter: 4, Answer: "old answer"},
			{Status: "idle", RunCounter: 4, Answer: "old answer"},
			{Status: "working", RunCounter: 5},
			{Status: "idle", RunCounter: 5, Answer: "new answer"},
		}, false},
		{"old failure survives enqueue until run starts", []workflows.State{
			{Status: "idle", RunCounter: 4, LastRunFailure: failure, Inbox: []workflows.MessageInput{follow}},
			{Status: "idle", RunCounter: 4, LastRunFailure: &workflows.Failure{Reason: "model_error", Detail: "offline"}, Inbox: []workflows.MessageInput{follow, own}},
			{Status: "working", RunCounter: 5, Inbox: []workflows.MessageInput{own}},
			{Status: "working", RunCounter: 6},
			{Status: "idle", RunCounter: 6, Answer: "new answer"},
		}, false},
		{"old failed status before new submit starts", []workflows.State{
			{Status: "failed", RunCounter: 4, LastRunFailure: failure},
			{Status: "failed", RunCounter: 4, LastRunFailure: failure},
			{Status: "working", RunCounter: 5},
			{Status: "idle", RunCounter: 5, Answer: "new answer"},
		}, false},
		{"new run fails holding own followup", []workflows.State{
			{Status: "idle", RunCounter: 4, LastRunFailure: failure, Inbox: []workflows.MessageInput{follow}},
			{Status: "working", RunCounter: 5, Inbox: []workflows.MessageInput{own}},
			{Status: "idle", RunCounter: 5, LastRunFailure: failure, Inbox: []workflows.MessageInput{own}},
		}, true},
		{"baseline active run fails without counter increment", []workflows.State{
			{Status: "working", RunCounter: 4},
			{Status: "working", RunCounter: 4, Inbox: []workflows.MessageInput{own}},
			{Status: "idle", RunCounter: 4, LastRunFailure: failure, Inbox: []workflows.MessageInput{own}},
		}, true},
		{"baseline active run finishes recording its failure", []workflows.State{
			{Status: "working", RunCounter: 4, LastRunFailure: failure},
			{Status: "idle", RunCounter: 4, LastRunFailure: failure, Inbox: []workflows.MessageInput{own}},
		}, true},
		{"baseline blocked run aborts holding own followup", []workflows.State{
			{Status: "blocked", RunCounter: 4, PendingApproval: &workflows.PendingApproval{RequestID: "approval"}},
			{Status: "idle", RunCounter: 4, LastRunFailure: &workflows.Failure{Reason: "aborted"}, Inbox: []workflows.MessageInput{own}},
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &cliFake{states: tc.states, disposition: "queued"}
			dir := t.TempDir()
			f.submit = func() {
				// Model durable acceptance of the actual randomly identified input,
				// followed by source-order draining/steer consumption on Query.
				for i := 1; i < len(f.states); i++ {
					for j, msg := range f.states[i].Inbox {
						if msg.RequestID == "own" {
							f.states[i].Inbox[j] = f.submitted
						}
					}
				}
				// Even a misleading parent display settlement cannot decide success.
				if _, err := events.NewStore(dir).Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.AgentSettled, events.None, events.AgentData{Status: "done"}); err != nil {
					t.Fatal(err)
				}
			}
			var out, diag bytes.Buffer
			err := runSession(context.Background(), f, config.Config{DataDir: dir}, runOptions{session: "s", prompt: "new", json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
			if tc.failed {
				if err == nil || !strings.Contains(err.Error(), "session failed") || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected authoritative failure, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if f.queries != len(tc.states) {
				t.Fatalf("settled too early: queries=%d want=%d", f.queries, len(tc.states))
			}
			lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
			var final cliSummary
			if len(lines) != 2 || json.Unmarshal(lines[len(lines)-1], &final) != nil || final.Type != "session_state" {
				t.Fatalf("invalid final output: %q", out.String())
			}
			if !tc.failed && final.Answer != "new answer" {
				t.Fatal(final)
			}
		})
	}
}

func TestWaitRetainedFailureQueue(t *testing.T) {
	for _, reason := range []string{"model_error", "aborted"} {
		for _, status := range []string{"idle", "failed"} {
			t.Run(status+"/"+reason, func(t *testing.T) {
				state := workflows.State{Status: status, RunCounter: 4, LastRunFailure: &workflows.Failure{Reason: reason}, Inbox: []workflows.MessageInput{{RequestID: "held"}}}
				f := &cliFake{states: []workflows.State{state}}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				got, err := pollSession(ctx, f, "s", "settled", time.Millisecond)
				if err != nil || f.queries != 1 {
					t.Fatalf("held queue did not settle: queries=%d err=%v", f.queries, err)
				}
				if err := settlementError(got); err == nil || !strings.Contains(err.Error(), reason) {
					t.Fatalf("wait must return failure: %v", err)
				}
			})
		}
	}
}

func TestHeldFailureStillRequiresQuiescence(t *testing.T) {
	for _, status := range []string{"working", "blocked"} {
		s := workflows.State{Status: status, RunCounter: 4, LastRunFailure: &workflows.Failure{Reason: "aborted"}, Inbox: []workflows.MessageInput{{RequestID: "held"}}}
		if sessionSettled(s) {
			t.Fatalf("active failure settled: %+v", s)
		}
	}
}
