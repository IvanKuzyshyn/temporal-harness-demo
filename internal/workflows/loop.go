package workflows

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// scenarioForRun never mutates the session configuration: UpdateWithStart does
// not replace it in an existing session. Legacy demo configuration applies only
// to the first logical run, including across Continue-As-New via RunCounter.
func scenarioForRun(configured string, run int, msg MessageInput) string {
	if !msg.Internal && !msg.ContextOnly {
		text := strings.ToLower(strings.TrimSpace(msg.Text))
		text = strings.ReplaceAll(text, "’", "'")
		text = strings.TrimRight(text, ".!?")
		switch text {
		case "/demo",
			"find what is broken in the sample project and verify the fix",
			"find what's broken in the sample project and verify the fix",
			"inspect calc and its tests, delegate a read-only review, propose correcting add with approval, then run go test ./... and summarize the result":
			return "demo"
		}
		if strings.EqualFold(configured, "demo") && run == 1 {
			return "demo"
		}
	}
	if strings.EqualFold(configured, "demo") {
		return ""
	}
	return configured
}

// Successful reads retain exact source bytes for fixture validation. Everything
// else carries explicit outcome metadata, especially silent nonzero commands and
// denials (which have no exitCode). Never infer success from an empty output.
func toolMessageContent(name string, res tools.Result) string {
	if name == "read_file" {
		// Retain the established recovered-read interruption marker, too.
		if res.Interrupted {
			return "[interrupted]\n" + res.Output
		}
		if !res.IsError {
			return res.Output
		}
	}
	content, err := json.Marshal(res)
	if err != nil {
		// Structured is optional tool-owned JSON. Retain all outcome fields even
		// if a restored/mocked result contains malformed structured data.
		res.Structured = nil
		content, _ = json.Marshal(res)
	}
	return string(content)
}

func (r *runtime) run(ctx workflow.Context, msg MessageInput) error {
	scenario := scenarioForRun(r.cfg.Scenario, r.state.RunCounter, msg)
	if err := r.emit(ctx, events.AgentStart, events.None, events.AgentData{InputID: r.inputID, Status: "working"}); err != nil {
		return err
	}
	if !msg.ContextOnly {
		if err := r.appendMessages(ctx, r.inputID+"/user", llm.Message{Role: "user", Content: msg.Text}); err != nil {
			return err
		}
		r.state.HasUnansweredContext = true
	}
	if err := r.gatherContext(ctx); err != nil {
		return err
	}
	for r.state.Turn < r.state.MaxTurns {
		if err := r.drainSteers(ctx); err != nil {
			return err
		}
		turn := r.state.Turn
		r.state.Turn++
		r.state.TotalTurns++
		if err := r.emit(ctx, events.TurnStart, events.None, events.TurnData{Turn: r.state.Turn}); err != nil {
			return err
		}
		llmCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			TaskQueue: LLMQueue, StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 30 * time.Minute,
			HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true,
			RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, NonRetryableErrorTypes: []string{"ModelNotFound", "InvalidRequest"}},
		})
		var result LLMResult
		// A canceled run must still join activity cleanup before the next run.
		waitCtx, _ := workflow.NewDisconnectedContext(ctx)
		err := workflow.ExecuteActivity(llmCtx, LLMActivity, LLMInput{Scope: r.scope, Model: r.cfg.Model, DataDir: r.cfg.DataDir, TranscriptRef: r.state.TranscriptRef, Turn: turn, Scenario: scenario, InputID: r.inputID, Tools: r.modelTools()}).Get(waitCtx, &result)
		if err != nil {
			reason := "model_error"
			if temporal.IsCanceledError(err) || ctx.Err() != nil {
				reason = "aborted"
			}
			r.fail(reason, err)
			_ = r.emit(ctx, events.TurnEnd, events.None, events.TurnData{Turn: r.state.Turn})
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.recordSeq(result.LastSeq)
		if len(result.ToolCalls) == 0 {
			// The activity has durably answered the transcript. Unconsumed steers
			// are queued work, not an unanswered transcript (even at the budget).
			r.state.Answer = bounded(result.Summary, maxAnswerBytes)
			r.state.HasUnansweredContext = false
			// TurnEnd can yield to a steer Update, including on a final answer.
			if err := r.emit(ctx, events.TurnEnd, events.None, events.TurnData{Turn: r.state.Turn}); err != nil {
				return err
			}
			if r.hasSteers() {
				continue
			}
			return nil
		}
		// Activities already enforce this, but guard the durable/public state
		// at the workflow boundary as well (including restored/mocked results).
		toolBytes := 0
		for _, call := range result.ToolCalls {
			toolBytes += len(call.ID) + len(call.Name) + len(call.Args)
		}
		_, encodeErr := json.Marshal(result.ToolCalls)
		if encodeErr != nil || len(result.ToolCalls) > 32 || toolBytes > 64<<10 {
			err := fmt.Errorf("model tool-call payload limit exceeded")
			r.fail("model_error", err)
			return err
		}
		r.pruneChildren()
		r.state.ActiveTools = result.ToolCalls
		batch, err := r.executeBatch(ctx, result.ToolCalls)
		if err != nil {
			return err
		}
		r.recordSeq(batch.LastSeq)
		messages := make([]llm.Message, 0, len(batch.Results))
		for i, res := range batch.Results {
			content := toolMessageContent(result.ToolCalls[i].Name, res)
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: res.ToolCallID, ToolName: result.ToolCalls[i].Name, Content: content})
		}
		if err = r.persistJoinedToolResults(ctx, fmt.Sprintf("%s/turn/%d/tools", r.inputID, turn), messages...); err != nil {
			return err
		}
		r.state.ActiveTools = nil
		r.pruneChildren()
		for _, res := range batch.Results {
			if res.Interrupted {
				err := fmt.Errorf("tool %s interrupted", res.ToolCallID)
				r.fail("interrupted", err)
				return err
			}
		}
		if err = r.emit(ctx, events.TurnEnd, events.None, events.TurnData{Turn: r.state.Turn}); err != nil {
			return err
		}
	}
	err := fmt.Errorf("maximum turns (%d) reached", r.state.MaxTurns)
	r.fail("turn_limit", err)
	return err
}
