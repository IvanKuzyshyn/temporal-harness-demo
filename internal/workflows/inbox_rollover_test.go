package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func carryFromTest(t *testing.T, env *testsuite.TestWorkflowEnvironment) Input {
	t.Helper()
	var can *workflow.ContinueAsNewError
	if err := env.GetWorkflowError(); !errors.As(err, &can) {
		t.Fatalf("expected CAN, got %v", err)
	}
	var input Input
	if err := converter.GetDefaultDataConverter().FromPayloads(can.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input.Carry == nil {
		t.Fatal("missing carry")
	}
	return input
}
func TestInboxContinueAsNewCarriesAndDeduplicates(t *testing.T) {
	for _, suggested := range []bool{false, true} {
		t.Run(fmt.Sprint(suggested), func(t *testing.T) {
			f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "answer"}, nil })
			f.onModel = func(n int) {
				f.send("next", "next", "followUp")
				if suggested {
					f.env.SetContinueAsNewSuggested(true)
				}
			}
			f.env.RegisterDelayedCallback(func() {
				// Omitted RequestID falls back to the actual SDK Update ID.
				f.env.UpdateWorkflowNoRejection(MessageUpdate, "first", t, MessageInput{Text: "first"})
			}, 0)
			input := Input{ContinueAfterTurns: 1}
			if suggested {
				input.ContinueAfterTurns = 100
			}
			f.env.ExecuteWorkflow(SessionWorkflow, input)
			carried := carryFromTest(t, f.env)
			s := carried.Carry
			if s.TotalTurns != 1 || s.RunCounter != 1 || s.TranscriptRef != "ref" || len(s.Inbox) != 1 || len(s.Receipts) != 2 || !s.ContextGathered || s.Answer != "answer" {
				t.Fatalf("carry=%+v", s)
			}
			// Simulate the next server execution; testsuite does not automatically follow CAN.
			g := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "next answer"}, nil })
			g.env.RegisterDelayedCallback(func() {
				g.env.UpdateWorkflowNoRejection(MessageUpdate, "first", t, MessageInput{Text: "first"})
				g.env.UpdateWorkflowNoRejection(MessageUpdate, "different-sdk-id", t, MessageInput{Text: "first", RequestID: "first"})
			}, 0)
			if suggested {
				g.env.RegisterDelayedCallback(func() {
					if g.models != 1 || g.state().TotalTurns != 2 || g.state().RunCounter != 2 {
						t.Errorf("second execution=%+v models=%d", g.state(), g.models)
					}
					g.env.CancelWorkflow()
				}, time.Second)
			}
			g.env.ExecuteWorkflow(SessionWorkflow, carried)
			if g.models != 1 || fmt.Sprint(g.users) != "[next]" {
				t.Errorf("immediate rollover/duplicate: models=%d users=%v", g.models, g.users)
			}
			if !suggested {
				next := carryFromTest(t, g.env)
				if next.Carry.TotalTurns != 2 || next.Carry.RunCounter != 2 {
					t.Errorf("second carry=%+v", next.Carry)
				}
			}
		})
	}
}
func TestInboxCANDrainsHandlersAndPreservesFailure(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	completed := false
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow("slow", "slow", &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(_ any, err error) {
			if err != nil {
				t.Error(err)
			}
			completed = true
		}})
	}, 0)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		r := &runtime{cfg: Input{ContinueAfterTurns: 1}, state: State{Status: "idle", TotalTurns: 21, RunCounter: 7, QueuePaused: true, HasUnansweredContext: true, LastRunFailure: &Failure{Reason: "interrupted"}, AfterRunCount: 2, OriginInputID: "origin", WroteFile: true, ApprovalCounter: 3, ChildCounter: 9, LastSeq: 50}, executionStartTurns: 20}
		started := false
		if err := workflow.SetUpdateHandler(ctx, "slow", func(ctx workflow.Context) error {
			started = true
			if err := workflow.Sleep(ctx, time.Second); err != nil {
				return err
			}
			r.state.Inbox = append(r.state.Inbox, MessageInput{Text: "retained"})
			return nil
		}); err != nil {
			return err
		}
		if err := workflow.Await(ctx, func() bool { return started }); err != nil {
			return err
		}
		return r.continueAsNew(ctx)
	})
	input := carryFromTest(t, env)
	s := input.Carry
	if !completed || len(s.Inbox) != 1 || !s.QueuePaused || !s.HasUnansweredContext || s.LastRunFailure.Reason != "interrupted" || s.AfterRunCount != 2 || s.OriginInputID != "origin" || !s.WroteFile || s.ApprovalCounter != 3 || s.ChildCounter != 9 || s.LastSeq != 50 {
		t.Fatalf("handler/carry: completed=%v state=%+v", completed, s)
	}
	f := newInboxFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "ok"}, nil })
	f.env.RegisterDelayedCallback(func() {
		if f.models != 0 || f.state().LastRunFailure.Reason != "interrupted" {
			t.Errorf("failed carry auto-ran: %+v", f.state())
		}
		f.env.CancelWorkflow()
	}, time.Second)
	f.env.ExecuteWorkflow(SessionWorkflow, input)
}
func TestInboxCANDefersLiveChildren(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		r := &runtime{cfg: Input{ContinueAfterTurns: 1}, state: State{TotalTurns: 1, Children: []ChildState{{WorkflowID: "live", Status: "working"}}}}
		if err := r.continueAsNew(ctx); err != nil {
			return fmt.Errorf("continued with live child: %w", err)
		}
		r.state.Children[0].Status = "done"
		return r.continueAsNew(ctx)
	})
	_ = carryFromTest(t, env)
}
func TestInboxBoundedReceiptsQueryAndProvenance(t *testing.T) {
	r := &runtime{state: State{Status: "working", AfterRunCount: 2, OriginInputID: "origin"}, deferringSteers: true}
	receipt, err := r.message(nil, MessageInput{Text: "late", WhenBusy: "steer", RequestID: "late"})
	if err != nil || receipt.Disposition != "queued" || r.hasSteers() {
		t.Fatalf("late steer=%+v err=%v", receipt, err)
	}
	for _, msg := range []MessageInput{{Text: "x", Internal: true}, {Text: "x", OriginInputID: "spoof"}, {Text: "x", ContextOnly: true}} {
		if r.validateMessage(msg) == nil {
			t.Fatal("accepted internal metadata")
		}
	}
	if err = r.queueInternalFollowUp("test failure"); err != nil {
		t.Fatal(err)
	}
	internal := r.state.Inbox[1]
	if !internal.Internal || internal.OriginInputID != "origin" || r.state.AfterRunCount != 2 {
		t.Fatalf("lost provenance: %+v", r.state)
	}
	// Retain only the last 64 accepted receipts, independent of drained inputs.
	r.state.Inbox = nil
	for i := 0; i < 1000; i++ {
		id := fmt.Sprint(i)
		if _, err := r.message(nil, MessageInput{Text: "x", RequestID: id}); err != nil {
			t.Fatal(err)
		}
		r.state.Inbox = nil
	}
	if len(r.state.Receipts) != maxRetainedReceipts || r.state.Receipts[0].RequestID != "936" {
		t.Fatalf("receipt window: %+v", r.state.Receipts)
	}
	for i := 0; i < 64; i++ {
		r.state.Inbox = append(r.state.Inbox, MessageInput{Text: "small"})
	}
	if _, err = r.message(nil, MessageInput{Text: "x", RequestID: "999"}); err != nil {
		t.Fatalf("retained retry rejected by full inbox: %v", err)
	}
	if _, err = r.message(nil, MessageInput{Text: "conflict", RequestID: "999"}); err == nil {
		t.Fatal("accepted ID conflict")
	}
	r.state.Inbox = nil
	msg := MessageInput{Text: strings.Repeat("\x01", 64<<10)}
	for r.inboxFits(msg) == nil {
		r.state.Inbox = append(r.state.Inbox, msg)
	}
	for i := 0; i < 200; i++ {
		r.state.Children = append(r.state.Children, ChildState{WorkflowID: strings.Repeat("\x01", 4000), SummaryRef: strings.Repeat("\x01", 4000), Status: "done"})
	}
	r.pruneChildren()
	if len(r.state.Children) != maxRetainedChildren {
		t.Fatal("unbounded retained children")
	}
	args := json.RawMessage(`"` + strings.Repeat("x", 100000) + `"`)
	r.state.ActiveTools = []llm.ToolCall{{ID: "active", Name: "read_file", Args: args}}
	r.state.PendingApproval = &PendingApproval{RequestID: "approval", Preview: strings.Repeat("\x01", 200000), Call: llm.ToolCall{ID: "unsafe", Args: args}, Decision: &DecisionInput{Reason: strings.Repeat("\x01", 200000)}}
	r.state.Answer = strings.Repeat("\x01", 100000)
	r.state.LastRunFailure = &Failure{Reason: "interrupted", Detail: strings.Repeat("\x01", 100000)}
	snapshot := r.queryState()
	b, err := json.Marshal(snapshot)
	if err != nil || len(b) > maxQueryJSONBytes {
		t.Fatalf("query bytes=%d error=%v", len(b), err)
	}
	if len(snapshot.Children) > 64 || len(snapshot.Receipts) > maxRetainedReceipts || len(snapshot.ActiveTools) > 32 {
		t.Fatal("unbounded public collections")
	}
	if len(r.state.PendingApproval.Call.Args) != len(args) || len(r.state.ActiveTools[0].Args) != len(args) {
		t.Fatal("query mutated execution/approval inputs")
	}
}
