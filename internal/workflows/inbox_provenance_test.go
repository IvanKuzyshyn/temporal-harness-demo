package workflows

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
)

func TestInboxInternalOriginBudgetAcrossUserAndCAN(t *testing.T) {
	r := &runtime{state: State{Status: "idle", OriginInputID: "old-origin", AfterRunCount: 2}}
	if _, err := r.message(nil, MessageInput{Text: "genuine user", RequestID: "genuine"}); err != nil {
		t.Fatal(err)
	}
	if err := r.queueInternalFollowUp("old-origin failure"); err != nil {
		t.Fatal(err)
	}
	// Captured now, not when dequeued, and independent of later state changes.
	r.state.AfterRunCount = 1
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "new user's answer"}, nil })
	f.expectCAN = true
	f.onModel = func(int) {
		s := f.state()
		if s.AfterRunCount != 0 || s.OriginInputID != runInputID(1, "genuine") {
			t.Errorf("genuine did not reset: %+v", s)
		}
	}
	f.env.ExecuteWorkflow(SessionWorkflow, Input{ContinueAfterTurns: 1, Carry: &r.state})
	carried := carryFromTest(t, f.env)
	if f.models != 1 || len(carried.Carry.Inbox) != 1 {
		t.Fatalf("first execution models=%d carry=%+v", f.models, carried.Carry)
	}
	queued := carried.Carry.Inbox[0]
	if !queued.Internal || queued.OriginInputID != "old-origin" || queued.AfterRunCount != 2 {
		t.Fatalf("lost carried provenance: %+v", queued)
	}
	g := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "fixed"}, nil })
	g.expectCAN = true
	g.onModel = func(int) {
		s := g.state()
		if s.AfterRunCount != 2 || s.OriginInputID != "old-origin" {
			t.Errorf("origin detached from budget: %+v", s)
		}
	}
	g.env.ExecuteWorkflow(SessionWorkflow, carried)
	next := carryFromTest(t, g.env)
	if g.models != 1 || next.Carry.AfterRunCount != 2 || next.Carry.OriginInputID != "old-origin" || len(next.Carry.Inbox) != 0 || next.Carry.RunCounter != 2 {
		t.Fatalf("internal execution models=%d carry=%+v", g.models, next.Carry)
	}
}

func TestInboxInternalOriginBudgetAcrossUserWithoutCAN(t *testing.T) {
	r := &runtime{state: State{Status: "idle", OriginInputID: "old-origin", AfterRunCount: 2}}
	if _, err := r.message(nil, MessageInput{Text: "genuine user", RequestID: "genuine"}); err != nil {
		t.Fatal(err)
	}
	if err := r.queueInternalFollowUp("old-origin failure"); err != nil {
		t.Fatal(err)
	}
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "answer"}, nil })
	f.onModel = func(n int) {
		s := f.state()
		wantOrigin, wantCount := runInputID(1, "genuine"), 0
		if n == 2 {
			wantOrigin, wantCount = "old-origin", 2
		}
		if s.OriginInputID != wantOrigin || s.AfterRunCount != wantCount {
			t.Errorf("model %d provenance=%+v", n, s)
		}
	}
	f.after(func() {
		s := f.state()
		if f.models != 2 || s.RunCounter != 2 || s.AfterRunCount != 2 || s.OriginInputID != "old-origin" {
			t.Errorf("final provenance=%+v models=%d", s, f.models)
		}
		f.env.CancelWorkflow()
	}, time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{Carry: &r.state})
}

func TestInboxContextContinuePreservesRestartCount(t *testing.T) {
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "fixed"}, nil })
	f.after(func() { f.env.UpdateWorkflowNoRejection(ContinueUpdate, "resume", t) }, 0)
	f.after(func() {
		s := f.state()
		if f.models != 1 || len(f.users) != 0 || s.AfterRunCount != 2 || s.OriginInputID != "origin" {
			t.Errorf("context-only provenance: %+v models=%d users=%v", s, f.models, f.users)
		}
		f.env.CancelWorkflow()
	}, time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{Carry: &State{OriginInputID: "origin", AfterRunCount: 2, HasUnansweredContext: true, QueuePaused: true}})
}

func TestInboxRestartCountAdmissionBounds(t *testing.T) {
	for _, count := range []int{-1, 0, 1, 2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := &runtime{state: State{OriginInputID: "origin", AfterRunCount: count}}
			err := r.queueInternalFollowUp("failure")
			valid := count >= 0 && count <= 2
			if (err == nil) != valid || (len(r.state.Inbox) == 1) != valid {
				t.Fatalf("count=%d queue=%+v error=%v", count, r.state.Inbox, err)
			}
			if valid && r.state.Inbox[0].AfterRunCount != count {
				t.Fatal("did not capture count")
			}
			if count != 0 {
				msg := MessageInput{Text: "spoof", AfterRunCount: count}
				if r.validateMessage(msg) == nil {
					t.Fatal("validator accepted external count")
				}
				if _, err := r.message(nil, msg); err == nil {
					t.Fatal("handler accepted external count")
				}
			}
		})
	}
	for _, queued := range []bool{false, true} {
		for _, count := range []int{-1, 3} {
			t.Run(fmt.Sprintf("carry/queued=%v/count=%d", queued, count), func(t *testing.T) {
				var suite testsuite.WorkflowTestSuite
				env := suite.NewTestWorkflowEnvironment()
				state := State{AfterRunCount: count}
				if queued {
					state = State{Inbox: []MessageInput{{Internal: true, OriginInputID: "origin", AfterRunCount: count, Text: "failure"}}}
				}
				env.ExecuteWorkflow(SessionWorkflow, Input{Carry: &state})
				if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "internal restart count") {
					t.Fatalf("invalid carried count accepted: %v", err)
				}
			})
		}
	}
}
