package workflows

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
)

// A positive process exit is not proof that RunCommand completed successfully:
// final log sync can fail after the process exits, retaining its exit/log details.
func TestAfterRunCommandActivityErrorDiscriminator(t *testing.T) {
	positive := testCommand(1)
	for _, tc := range []struct {
		name         string
		err          error
		recovery     bool
		recoveryFail bool
	}{
		{name: "completed_positive_exit_repairs"},
		{name: "positive_exit_log_sync_error", err: temporal.NewNonRetryableApplicationError("sync command log failed", "CommandError", nil, positive)},
		{name: "error_without_details", err: temporal.NewNonRetryableApplicationError("runner unavailable", "CommandError", nil)},
		{name: "timeout_recovered", err: temporal.NewTimeoutError(enums.TIMEOUT_TYPE_HEARTBEAT, nil), recovery: true},
		{name: "canceled_recovered", err: temporal.NewCanceledError(), recovery: true},
		{name: "timeout_recovery_failed", err: temporal.NewTimeoutError(enums.TIMEOUT_TYPE_HEARTBEAT, nil), recovery: true, recoveryFail: true},
		{name: "canceled_recovery_failed", err: temporal.NewCanceledError(), recovery: true, recoveryFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAfterRunFixture(t, writeThenAnswer, func(in ToolInput) (CommandResult, error) {
				if in.InputID != runInputID(1, "original") {
					return testCommand(0), nil
				}
				if tc.err != nil {
					return CommandResult{}, tc.err
				}
				return positive, nil
			})
			if tc.recovery {
				result := positive
				var err error
				if tc.recoveryFail {
					result = CommandResult{}
					err = temporal.NewNonRetryableApplicationError("recovery unavailable", "RecoveryError", nil)
				}
				f.env.OnActivity(RecoverCommandActivity, mock.Anything, mock.Anything).Return(result, err).Once()
			}
			f.env.RegisterDelayedCallback(func() { f.send("original") }, 0)
			f.run(Input{}, func() {
				s := f.state()
				wantCommands, wantModels, wantBudget := 1, 2, 0
				if tc.err == nil {
					wantCommands, wantModels, wantBudget = 2, 3, 1
					if s.LastRunFailure != nil || s.QueuePaused || s.VerificationPending || s.HasUnansweredContext {
						t.Errorf("normal positive exit did not repair and reverify: %+v", s)
					}
				} else if s.LastRunFailure == nil || !s.QueuePaused || !s.VerificationPending || !s.HasUnansweredContext {
					t.Errorf("activity failure did not pause for explicit user action: %+v", s)
				}
				if f.commands != wantCommands || f.models != wantModels || f.approvals != wantCommands || s.AfterRunCount != wantBudget || len(s.Inbox) != 0 || len(f.pairs) != wantCommands {
					t.Fatalf("commands=%d models=%d approvals=%d pairs=%d state=%+v", f.commands, f.models, f.approvals, len(f.pairs), s)
				}
				var outcome verificationResult
				if err := json.Unmarshal([]byte(f.pairs[0].Messages[1].Content), &outcome); err != nil {
					t.Fatal(err)
				}
				if outcome.ExecutionError != (tc.err != nil) || !outcome.IsError || outcome.Interrupted != tc.recovery {
					t.Errorf("lost activity completion discriminator: %+v", outcome)
				}
				if tc.err == nil || tc.name == "positive_exit_log_sync_error" || (tc.recovery && !tc.recoveryFail) {
					if outcome.ExitCode == nil || *outcome.ExitCode != positive.ExitCode || outcome.LogRef != positive.LogRef || outcome.Tail != positive.Tail {
						t.Errorf("lost positive exit/log details: %+v", outcome)
					}
				}
				f.assertPairs()
			})
			f.env.AssertExpectations(t)
		})
	}
}
