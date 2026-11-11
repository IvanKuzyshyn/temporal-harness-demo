package workflows

import (
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
)

// Retry infrastructure exhaustion, not cancellation or a declared permanent
// defect. Each call remains a bounded activity retry batch recorded in history.
func retryWebStorage(err error) bool {
	if err == nil || temporal.IsCanceledError(err) {
		return false
	}
	var app *temporal.ApplicationError
	if errors.As(err, &app) && app.NonRetryable() {
		return false
	}
	var panicErr *temporal.PanicError
	if errors.As(err, &panicErr) {
		return false
	}
	var act *temporal.ActivityError
	if errors.As(err, &act) && act.RetryState() == enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE {
		return false
	}
	return true
}
func webRecoveryDelay(previous time.Duration) time.Duration {
	if previous < 5*time.Second {
		return 5 * time.Second
	}
	if previous >= 30*time.Second {
		return time.Minute
	}
	return previous * 2
}
