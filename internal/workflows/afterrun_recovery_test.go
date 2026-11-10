package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
)

func TestAfterRunInterruptedAndInfrastructureNoAuto(t *testing.T) {
	cases := []struct {
		name   string
		result CommandResult
		err    error
	}{
		{"interrupted", CommandResult{ExitCode: 1, Interrupted: true, LogRef: "logs/partial", Tail: "partial"}, nil},
		{"unavailable", CommandResult{ExitCode: -1, LogRef: "logs/failed"}, nil},
		{"no_log", CommandResult{ExitCode: 1}, nil},
		{"no_result", CommandResult{}, nil},
		{"infrastructure", CommandResult{}, temporal.NewNonRetryableApplicationError("runner unavailable", "command", nil)},
		{"hung", CommandResult{}, temporal.NewNonRetryableApplicationError("hung", "command", nil, CommandResult{ExitCode: -1, LogRef: "logs/hung", Tail: "last output"})},
		{"timeout", CommandResult{}, temporal.NewTimeoutError(enums.TIMEOUT_TYPE_HEARTBEAT, nil)},
		{"canceled", CommandResult{}, temporal.NewCanceledError()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAfterRunFixture(t, writeThenAnswer, func(ToolInput) (CommandResult, error) { return tc.result, tc.err })
			f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
			f.run(Input{}, func() {
				s := f.state()
				if f.commands != 1 || f.models != 2 || len(s.Inbox) != 0 || s.AfterRunCount != 0 || !s.QueuePaused || !s.VerificationPending || !s.HasUnansweredContext || s.LastRunFailure == nil {
					t.Errorf("unexpected retry/settlement: %+v calls=%d models=%d", s, f.commands, f.models)
				}
				if len(f.pairs) != 1 {
					t.Fatal("missing joined outcome")
				}
				f.assertPairs()
			})
		})
	}
}

func TestAfterRunContinuePreservesPendingAndExhaustedBudget(t *testing.T) {
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			f := newAfterRunFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "no further write"}, nil }, func(ToolInput) (CommandResult, error) { return testCommand(exit), nil })
			f.env.RegisterDelayedCallback(func() { f.env.UpdateWorkflowNoRejection(ContinueUpdate, "continue", t) }, 0)
			f.run(Input{Carry: &State{OriginInputID: "old-origin", AfterRunCount: 2, VerificationPending: true, HasUnansweredContext: true, QueuePaused: true}}, func() {
				s := f.state()
				if f.models != 1 || f.commands != 1 || s.AfterRunCount != 2 || s.OriginInputID != "old-origin" || s.WroteFile {
					t.Errorf("Continue reset origin: %+v", s)
				}
				if exit == 1 && (s.LastRunFailure == nil || s.LastRunFailure.Reason != "restart_limit" || !s.VerificationPending || !s.HasUnansweredContext) {
					t.Errorf("limit escaped: %+v", s)
				}
				if exit == 0 && (s.VerificationPending || s.HasUnansweredContext || s.LastRunFailure != nil) {
					t.Errorf("success pending: %+v", s)
				}
			})
		})
	}
}

func TestAfterRunPendingSnapshotAcrossGenuineInputAndCAN(t *testing.T) {
	r := &runtime{state: State{OriginInputID: "old-origin", AfterRunCount: 2, VerificationPending: true}}
	if _, err := r.message(nil, MessageInput{Text: "genuine", RequestID: "genuine"}); err != nil {
		t.Fatal(err)
	}
	if err := r.queueInternalFollowUp("old failure"); err != nil {
		t.Fatal(err)
	}
	// Real user work ahead of the old fix resets its own pending/budget, not the snapshot.
	f := newAfterRunFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "answer"}, nil }, func(ToolInput) (CommandResult, error) {
		t.Error("new user should not inherit verification")
		return CommandResult{}, nil
	})
	f.onStart = func(name string) {
		if name == LLMActivity {
			if s := f.state(); s.VerificationPending || s.AfterRunCount != 0 {
				t.Errorf("new origin not reset: %+v", s)
			}
		}
	}
	f.env.ExecuteWorkflow(SessionWorkflow, Input{ContinueAfterTurns: 1, Carry: &r.state})
	carried := carryFromTest(t, f.env)
	if f.commands != 0 || len(carried.Carry.Inbox) != 1 || !carried.Carry.Inbox[0].VerificationPending {
		t.Fatalf("lost queued pending: %+v", carried.Carry)
	}
	g := newAfterRunFixture(t, func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "still no write"}, nil }, func(ToolInput) (CommandResult, error) { return testCommand(1), nil })
	carried.HangTimeout = 17 * time.Second
	g.env.ExecuteWorkflow(SessionWorkflow, carried)
	next := carryFromTest(t, g.env)
	if g.commands != 1 || next.Carry.AfterRunCount != 2 || !next.Carry.VerificationPending || !next.Carry.HasUnansweredContext || !next.Carry.QueuePaused || next.Carry.LastRunFailure == nil || next.Carry.LastRunFailure.Reason != "restart_limit" || next.Carry.OriginInputID != "old-origin" {
		t.Errorf("lost restored pending/budget: %+v", next.Carry)
	}
}

func TestAfterRunPendingSurvivesInterleavedUser(t *testing.T) {
	f := newAfterRunFixture(t, writeThenAnswer, func(in ToolInput) (CommandResult, error) {
		exit := 0
		if in.InputID == runInputID(1, "original") {
			exit = 1
		}
		return testCommand(exit), nil
	})
	queued := false
	f.onStart = func(name string) {
		if name == NotifyActivity && f.approvals == 1 && !queued {
			queued = true
			f.send("genuine")
		}
		if name == LLMActivity && f.models == 3 {
			s := f.state()
			if s.VerificationPending || s.AfterRunCount != 0 || s.OriginInputID != runInputID(2, "genuine") {
				t.Errorf("new user inherited old origin: %+v", s)
			}
		}
		if name == LLMActivity && f.models == 4 {
			s := f.state()
			if !s.VerificationPending || s.AfterRunCount != 1 || s.OriginInputID != runInputID(1, "original") {
				t.Errorf("old pending lost: %+v", s)
			}
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
	f.run(Input{}, func() {
		s := f.state()
		if !queued || f.models != 4 || f.commands != 2 || f.ends != 3 || f.settled != 1 || s.VerificationPending || s.LastRunFailure != nil {
			t.Errorf("interleaved chain: %+v models=%d commands=%d", s, f.models, f.commands)
		}
	})
}

func TestAfterRunExternalPendingMetadataRejected(t *testing.T) {
	r := &runtime{}
	msg := MessageInput{Text: "spoof", VerificationPending: true}
	if r.validateMessage(msg) == nil {
		t.Error("validator accepted internal pending")
	}
	if _, err := r.message(nil, msg); err == nil {
		t.Error("handler accepted internal pending")
	}
}

func TestAfterRunQueueFullRollsBackBudget(t *testing.T) {
	f := newAfterRunFixture(t, writeThenAnswer, func(ToolInput) (CommandResult, error) { return testCommand(1), nil })
	f.onStart = func(name string) {
		if name == CommandActivity {
			for i := 0; i < maxInboxCount; i++ {
				f.send(fmt.Sprintf("queued-%d", i))
			}
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
	f.run(Input{}, func() {
		s := f.state()
		if s.AfterRunCount != 0 || len(s.Inbox) != maxInboxCount || !s.QueuePaused || !s.VerificationPending || !s.HasUnansweredContext || s.LastRunFailure == nil || f.models != 2 || f.commands != 1 {
			t.Errorf("admission failure: %+v", s)
		}
	})
}

func TestAfterRunStopDuringCommandAndPersistence(t *testing.T) {
	for _, where := range []string{"command", "persistence"} {
		t.Run(where, func(t *testing.T) {
			f := newAfterRunFixture(t, writeThenAnswer, func(ToolInput) (CommandResult, error) { return testCommand(1), nil })
			if where == "command" {
				f.env.OnActivity(CommandActivity, mock.Anything, mock.Anything).After(time.Hour).Return(testCommand(1), nil)
			}
			if where == "persistence" {
				f.env.OnActivity(TranscriptActivity, mock.Anything, mock.MatchedBy(func(in AppendInput) bool { return in.Messages[0].Role != "assistant" })).Return(AppendResult{TranscriptRef: "ref"}, nil)
				f.env.OnActivity(TranscriptActivity, mock.Anything, mock.MatchedBy(func(in AppendInput) bool { return len(in.Messages) == 2 && in.Messages[0].Role == "assistant" })).After(time.Second).Return(AppendResult{TranscriptRef: "ref"}, nil)
			}
			stopped := false
			f.onStart = func(name string) {
				if !stopped && ((where == "command" && name == CommandActivity) || (where == "persistence" && name == TranscriptActivity && len(f.pairs) > 0)) {
					stopped = true
					f.env.UpdateWorkflowNoRejection(StopUpdate, "stop", t)
				}
			}
			f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
			f.env.RegisterDelayedCallback(func() {
				s := f.state()
				if !stopped || s.LastRunFailure == nil || s.LastRunFailure.Reason != "aborted" || !s.QueuePaused || !s.VerificationPending || !s.HasUnansweredContext || s.AfterRunCount != 0 || len(f.pairs) != 1 || f.models != 2 {
					t.Errorf("stop state: %+v pairs=%d", s, len(f.pairs))
				}
				f.env.UpdateWorkflowNoRejection(ContinueUpdate, "continue", t)
			}, 3*time.Second)
			// The resumed no-write answer must reverify, through a NEW approval. Deny
			// that approval to prove Continue does not itself authorize execution.
			f.env.RegisterDelayedCallback(func() { f.decision = "deny" }, 2*time.Second)
			f.run(Input{}, func() {
				s := f.state()
				if f.models != 3 || f.commands != 1 || f.approvals != 2 || len(f.pairs) != 2 || !s.VerificationPending || s.AfterRunCount != 0 {
					t.Errorf("resumption escaped verification: %+v models=%d commands=%d", s, f.models, f.commands)
				}
				f.assertPairs()
			})
		})
	}
}

func TestAfterRunPersistenceFailureFailsClosed(t *testing.T) {
	f := newAfterRunFixture(t, writeThenAnswer, func(ToolInput) (CommandResult, error) { return testCommand(1), nil })
	f.env.OnActivity(TranscriptActivity, mock.Anything, mock.MatchedBy(func(in AppendInput) bool { return in.Messages[0].Role != "assistant" })).Return(AppendResult{TranscriptRef: "ref"}, nil)
	f.env.OnActivity(TranscriptActivity, mock.Anything, mock.MatchedBy(func(in AppendInput) bool { return len(in.Messages) == 2 && in.Messages[0].Role == "assistant" })).Return(AppendResult{}, temporal.NewApplicationError("store unavailable", "StoreUnavailable")).Times(3)
	stopped := false
	f.onStart = func(name string) {
		if name == TranscriptActivity && len(f.pairs) > 0 && !stopped {
			stopped = true
			f.env.UpdateWorkflowNoRejection(StopUpdate, "stop", t)
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{HangTimeout: 17 * time.Second})
	var app *temporal.ApplicationError
	if !errors.As(f.env.GetWorkflowError(), &app) || app.Type() != "ToolResultPersistence" || !app.NonRetryable() {
		t.Fatalf("fatal persistence: %v", f.env.GetWorkflowError())
	}
	s := f.state()
	if !stopped || s.Status != "failed" || !s.QueuePaused || !s.VerificationPending || !s.HasUnansweredContext || s.LastRunFailure == nil || s.LastRunFailure.Reason != "interrupted" || len(s.ActiveTools) != 0 || s.PendingApproval != nil || f.models != 2 || f.commands != 1 || f.ends != 0 || f.settled != 0 {
		t.Errorf("failed-open persistence: %+v models=%d", s, f.models)
	}
	f.env.AssertExpectations(t)
}

func TestAfterRunFailedModelAndTurnLimitSkip(t *testing.T) {
	for _, mode := range []string{"model", "turn_limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newAfterRunFixture(t, func(in LLMInput) (LLMResult, error) {
				if in.Turn == 0 {
					return writeThenAnswer(in)
				}
				return LLMResult{}, temporal.NewNonRetryableApplicationError("model failed", "InvalidRequest", nil)
			}, func(ToolInput) (CommandResult, error) {
				t.Error("verification after failed run")
				return CommandResult{}, nil
			})
			input := Input{}
			if mode == "turn_limit" {
				input.MaxTurns = 1
			}
			f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
			f.run(input, func() {
				s := f.state()
				if !s.WroteFile || s.LastRunFailure == nil || f.commands != 0 || len(f.pairs) != 0 {
					t.Errorf("failed run verified: %+v", s)
				}
			})
		})
	}
}

func TestAfterRunBoundedOutcomeAndUnavailableExit(t *testing.T) {
	for _, code := range []string{"", "unknown", "1", "0", "-1"} {
		result := tools.Result{IsError: true, Details: map[string]string{"exitCode": code, "logRef": strings.Repeat("r", 10000)}, Output: strings.Repeat("尾", 10000)}
		out := verificationOutcome(result)
		if (out.ExitCode == nil) != (code == "" || code == "unknown") || !out.Truncated || len(out.Tail) > 8192 || len(out.LogRef) > 2048 {
			t.Errorf("outcome bounds: %+v", out)
		}
		if _, err := json.Marshal(out); err != nil {
			t.Fatal(err)
		}
	}
	// Optional executor error metadata must survive independently of a positive
	// process exit. The production marker producer is an integration seam need.
	out := verificationOutcome(tools.Result{IsError: true, Details: map[string]string{"exitCode": "1", "logRef": "logs/test", "executionError": "true"}})
	if !out.ExecutionError {
		t.Error("activity error discriminator lost")
	}
	// Near-full answers retain the truthful status without carrying logs/history.
	r := &runtime{state: State{Answer: strings.Repeat("a", maxAnswerBytes)}}
	r.verificationStatus("go test ./... passed")
	if len(r.state.Answer) > maxAnswerBytes || !strings.HasSuffix(r.state.Answer, "go test ./... passed") {
		t.Error("status lost/bounds exceeded")
	}
}

// A disconnected append is awaited even when the entire workflow is canceled;
// it must not queue a fix or emit settlement after that cancellation.
func TestAfterRunGlobalCancellationPersistsPair(t *testing.T) {
	f := newAfterRunFixture(t, writeThenAnswer, func(ToolInput) (CommandResult, error) { return testCommand(1), nil })
	f.env.OnActivity(CommandActivity, mock.Anything, mock.Anything).After(time.Hour).Return(testCommand(1), nil)
	f.onStart = func(name string) {
		if name == CommandActivity {
			f.env.CancelWorkflow()
		}
	}
	f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
	f.env.ExecuteWorkflow(SessionWorkflow, Input{HangTimeout: 17 * time.Second})
	if !temporal.IsCanceledError(f.env.GetWorkflowError()) || len(f.pairs) != 1 || f.models != 2 || f.ends != 0 || f.settled != 0 {
		t.Errorf("cancellation tail missing: %v pairs=%d", f.env.GetWorkflowError(), len(f.pairs))
	}
	f.assertPairs()
}
