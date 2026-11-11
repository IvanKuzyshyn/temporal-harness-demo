package workflows

import (
	"fmt"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func (r *runtime) run(ctx workflow.Context, msg MessageInput) error {
	if err := r.emit(ctx, events.AgentStart, events.None, events.AgentData{InputID: r.inputID, Status: "working"}); err != nil {
		return err
	}
	if err := r.appendMessages(ctx, r.inputID+"/user", llm.Message{Role: "user", Content: msg.Text}); err != nil {
		return err
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
		err := workflow.ExecuteActivity(llmCtx, LLMActivity, LLMInput{Scope: r.scope, Model: r.cfg.Model, DataDir: r.cfg.DataDir, TranscriptRef: r.state.TranscriptRef, Turn: turn, Scenario: r.cfg.Scenario, InputID: r.inputID, Tools: r.modelTools()}).Get(waitCtx, &result)
		if err != nil {
			reason := "model_error"
			if temporal.IsCanceledError(err) || ctx.Err() != nil {
				reason = "aborted"
			}
			r.fail(reason, err)
			_ = r.emit(ctx, events.TurnEnd, events.None, events.TurnData{Turn: r.state.Turn})
			return err
		}
		r.recordSeq(result.LastSeq)
		if len(result.ToolCalls) == 0 {
			r.state.Answer = bounded(result.Summary, maxAnswerBytes)
			return r.emit(ctx, events.TurnEnd, events.None, events.TurnData{Turn: r.state.Turn})
		}
		r.state.ActiveTools = result.ToolCalls
		batch, err := r.executeBatch(ctx, result.ToolCalls)
		if err != nil {
			return err
		}
		r.recordSeq(batch.LastSeq)
		messages := make([]llm.Message, 0, len(batch.Results))
		for i, res := range batch.Results {
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: res.ToolCallID, ToolName: result.ToolCalls[i].Name, Content: res.Output})
		}
		if err = r.appendMessages(ctx, fmt.Sprintf("%s/turn/%d/tools", r.inputID, turn), messages...); err != nil {
			return err
		}
		r.state.ActiveTools = nil
		if err = r.emit(ctx, events.TurnEnd, events.None, events.TurnData{Turn: r.state.Turn}); err != nil {
			return err
		}
	}
	err := fmt.Errorf("maximum turns (%d) reached", r.state.MaxTurns)
	r.fail("turn_limit", err)
	return err
}
