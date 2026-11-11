package workflows

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/workflow"
)

// afterRun runs before agent_end/agent_settled while Status is still working.
// It never executes commands outside the normal preflight/approval/batch path.
func (r *runtime) afterRun(ctx workflow.Context) error {
	if !r.state.WroteFile && !r.state.VerificationPending {
		return nil
	}
	r.state.VerificationPending = true
	r.state.HasUnansweredContext = true
	call := llm.ToolCall{ID: r.inputID + "/afterrun/tests", Name: "run_command", Args: json.RawMessage(`{"cmd":"go","args":["test","./..."]}`)}
	r.state.ActiveTools = []llm.ToolCall{call}
	batch, err := r.executeBatch(ctx, []llm.ToolCall{call})
	if err != nil {
		return err
	}
	r.recordSeq(batch.LastSeq)
	result := batch.Results[0]
	if ctx.Err() != nil {
		result.IsError = true
		result.Interrupted = true
	}
	outcome := verificationOutcome(result)
	content, _ := json.Marshal(outcome) // Fixed struct containing only JSON primitives.
	// Do not leave an assistant intent dangling during approval, execution or
	// cancellation. Append the entire synthetic exchange only after joining.
	if err = r.persistJoinedToolResults(ctx, r.inputID+"/afterrun/tests", llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, llm.Message{Role: "tool", ToolCallID: call.ID, ToolName: call.Name, Content: string(content)}); err != nil {
		return err
	}
	r.state.ActiveTools = nil
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if outcome.ExecutionError || outcome.Interrupted || outcome.ExitCode == nil || *outcome.ExitCode < 0 || strings.TrimSpace(outcome.LogRef) == "" {
		return fmt.Errorf("verification did not complete: %s", content)
	}
	if *outcome.ExitCode == 0 {
		if outcome.IsError {
			return fmt.Errorf("verification execution failed: %s", content)
		}
		r.state.VerificationPending = false
		r.state.HasUnansweredContext = false
		r.verificationStatus("go test ./... passed (exitCode=0; logRef=" + outcome.LogRef + ")")
		return nil
	}
	// Only a completed positive test exit is eligible for automatic repair.
	// Denial, missing results, infrastructure failures and interruptions never
	// consume the automatic budget; they leave context resumable by the user.
	r.verificationStatus(fmt.Sprintf("go test ./... failed (exitCode=%d; logRef=%s)", *outcome.ExitCode, outcome.LogRef))
	if r.state.AfterRunCount >= maxAfterRunCount {
		err = fmt.Errorf("go test ./... failed after %d automatic fix runs; explicit Continue or new input required", maxAfterRunCount)
		r.fail("restart_limit", err)
		return err
	}
	text := fmt.Sprintf("Automatic verification failed for origin %s. Fix the failing tests; verification remains required even if you make no further write. Automatic fix %d of %d.\nCommand: go test ./...\n%s", bounded(r.state.OriginInputID, 512), r.state.AfterRunCount+1, maxAfterRunCount, content)
	r.state.AfterRunCount++ // queueInternalFollowUp snapshots the consumed count.
	if err = r.queueInternalFollowUp(text); err != nil {
		r.state.AfterRunCount-- // Only successfully admitted automatic work costs budget.
		return fmt.Errorf("queue verification fix: %w", err)
	}
	return nil
}

// Keep explicit outcome metadata in the model-visible result, not just stdout.
// Missing exit codes stay null rather than being mistaken for a passing test.
type verificationResult struct {
	ExitCode    *int `json:"exitCode"`
	IsError     bool `json:"isError"`
	Interrupted bool `json:"interrupted"`
	// The executor may retain activity failure separately from a process exit.
	ExecutionError bool   `json:"executionError"`
	LogRef         string `json:"logRef"`
	Tail           string `json:"tail"`
	Truncated      bool   `json:"truncated"`
}

func verificationOutcome(result tools.Result) verificationResult {
	out := verificationResult{IsError: result.IsError, Interrupted: result.Interrupted, ExecutionError: result.Details["executionError"] == "true", LogRef: bounded(result.Details["logRef"], 2048), Tail: bounded(result.Output, 8192), Truncated: result.Details["truncated"] == "true" || len(result.Output) > 8192}
	if exit, err := strconv.Atoi(result.Details["exitCode"]); err == nil {
		out.ExitCode = &exit
	}
	return out
}
func (r *runtime) verificationStatus(status string) {
	suffix := "\n\nVerification: " + status
	r.state.Answer = bounded(r.state.Answer, maxAnswerBytes-len(suffix)) + suffix
}
