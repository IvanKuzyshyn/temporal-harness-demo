package workflows

import (
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
)

// A final answer clears unanswered context before TurnEnd yields. Stop there
// must preserve both an existing obligation and a write not yet seen by afterRun.
func TestAfterRunFinalAnswerStopResumesVerification(t *testing.T) {
	for _, mode := range []string{"context_continue", "queued_automatic_fix", "initial_write"} {
		t.Run(mode, func(t *testing.T) {
			model := func(LLMInput) (LLMResult, error) { return LLMResult{Summary: "no further write"}, nil }
			origin, count := "old-origin", 1
			input := Input{Carry: &State{OriginInputID: origin, AfterRunCount: count, VerificationPending: true, HasUnansweredContext: true, QueuePaused: true}}
			beforeModels, beforeCommands := 1, 0
			if mode != "context_continue" {
				model, input = writeThenAnswer, Input{}
				origin = runInputID(1, "original")
				beforeModels, beforeCommands = 3, 1
				if mode == "initial_write" {
					beforeModels, beforeCommands, count = 2, 0, 0
				}
			}
			f := newAfterRunFixture(t, model, func(in ToolInput) (CommandResult, error) {
				if mode == "queued_automatic_fix" && in.InputID == origin {
					return testCommand(1), nil
				}
				return testCommand(0), nil
			})
			stopped, approvedResume := false, false
			priorApproval := ""
			f.onStart = func(name string) {
				s := f.state()
				if name == NotifyActivity && !stopped && s.PendingApproval != nil {
					priorApproval = s.PendingApproval.RequestID
				}
				// The first emit after Answer is assigned is final-answer TurnEnd.
				if name == EmitActivity && !stopped && f.models == beforeModels && s.Answer != "" {
					stopped = true
					if mode == "initial_write" && (!s.WroteFile || s.VerificationPending) {
						t.Fatalf("did not stop before FIRST afterRun: %+v", s)
					}
					f.env.UpdateWorkflowNoRejection(StopUpdate, "stop-final-answer", t)
				}
			}
			f.env.RegisterDelayedCallback(func() {
				if mode == "context_continue" {
					f.env.UpdateWorkflowNoRejection(ContinueUpdate, "first-continue", t)
				} else {
					f.send("original")
				}
			}, 0)
			f.env.RegisterDelayedCallback(func() {
				s := f.state()
				if !stopped || !s.VerificationPending || !s.HasUnansweredContext || s.LastRunFailure == nil || s.LastRunFailure.Reason != "aborted" || !s.QueuePaused || len(s.Inbox) != 0 || s.AfterRunCount != count || s.OriginInputID != origin {
					t.Errorf("lost obligation/provenance at aborted settlement: %+v", s)
				}
				if f.models != beforeModels || f.commands != beforeCommands || f.approvals != beforeCommands || len(f.pairs) != beforeCommands {
					t.Errorf("afterRun executed on aborted run: models=%d commands=%d approvals=%d pairs=%d", f.models, f.commands, f.approvals, len(f.pairs))
				}
				f.decision = "wait" // Resume must block on a fresh human approval.
				f.env.UpdateWorkflow(ContinueUpdate, "resume-verification", &testsuite.TestUpdateCallback{
					OnReject: func(err error) { t.Errorf("Continue rejected: %v", err) },
					OnComplete: func(_ any, err error) {
						if err != nil {
							t.Errorf("Continue failed: %v", err)
						}
					},
				})
			}, 3*time.Second)
			f.env.RegisterDelayedCallback(func() {
				s := f.state()
				p := s.PendingApproval
				if p == nil || p.Call.Name != "run_command" || p.RequestID == priorApproval || f.commands != beforeCommands || f.approvals != beforeCommands+1 || s.AfterRunCount != count || s.OriginInputID != origin || !s.VerificationPending {
					t.Errorf("resume did not preserve provenance and require fresh approval: %+v", s)
					return
				}
				approvedResume = true
				f.env.UpdateWorkflowNoRejection(DecideUpdate, "approve-resumed-tests", t, DecisionInput{RequestID: p.RequestID, ActionHash: p.ActionHash, Approved: true, User: "human"})
			}, 3500*time.Millisecond)
			f.run(input, func() {
				s := f.state()
				if !approvedResume || f.models != beforeModels+1 || f.commands != beforeCommands+1 || f.approvals != beforeCommands+1 || len(f.pairs) != beforeCommands+1 || s.VerificationPending || s.HasUnansweredContext || s.LastRunFailure != nil || s.AfterRunCount != count || s.OriginInputID != origin || s.QueuePaused || len(s.Inbox) != 0 || s.WroteFile {
					t.Errorf("Continue did not complete no-write re-verification: models=%d commands=%d state=%+v", f.models, f.commands, s)
				}
				f.assertPairs()
			})
		})
	}
}
