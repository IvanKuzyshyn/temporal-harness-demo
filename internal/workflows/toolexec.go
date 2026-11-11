package workflows

import (
	"errors"
	"fmt"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Only static metadata is accessed in workflow code; path/schema policy runs in activities.
func (r *runtime) modelTools() []llm.ToolDefinition {
	var registry tools.LocalRegistry
	defs := registry.Definitions("model")
	out := make([]llm.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.ToolDefinition)
	}
	return out
}

func (r *runtime) beforeTool(ctx workflow.Context, call llm.ToolCall, p PreflightResult) (bool, string) {
	var registry tools.LocalRegistry
	def, ok := registry.Lookup(call.Name)
	if !ok {
		return false, "unknown tool"
	}
	if !p.Allowed {
		return false, p.Reason
	}
	if p.Mode != def.ExecutionMode || p.Replay != def.Replay || p.ActionHash == "" {
		return false, "invalid preflight metadata"
	}
	if p.RequiresApproval || def.Replay == tools.Unsafe {
		allowed, err := r.approveTool(ctx, call)
		if err != nil {
			return false, err.Error()
		}
		if !allowed {
			return false, "tool approval denied"
		}
	}
	return true, ""
}

func (r *runtime) executeBatch(ctx workflow.Context, calls []llm.ToolCall) (BatchResult, error) {
	batch := BatchResult{Results: make([]tools.Result, len(calls))}
	preflight := make([]PreflightResult, len(calls))
	allowed := make([]bool, len(calls))
	sequential := false
	ids := make([]string, len(calls))
	var registry tools.LocalRegistry
	input := func(call llm.ToolCall) ToolInput {
		return ToolInput{InputID: r.inputID, Scope: r.scope, DataDir: r.cfg.DataDir, Workdir: r.cfg.Workdir, Call: call}
	}
	for i, call := range calls {
		ids[i] = call.ID
		def, ok := registry.Lookup(call.Name)
		if !ok || def.ExecutionMode == tools.Sequential {
			sequential = true
		}
		err := workflow.ExecuteActivity(activityContext(ctx), PreflightActivity, input(call)).Get(ctx, &preflight[i])
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			allowed[i], reason = r.beforeTool(ctx, call, preflight[i])
		}
		batch.Results[i] = tools.Result{ToolCallID: call.ID, IsError: true, Output: bounded(reason, 8192)}
	}
	concurrency := 4
	if sequential {
		concurrency = 1
	}
	_ = r.emit(ctx, events.ParallelStart, events.Parallel, events.ParallelData{ToolCallIDs: ids, Concurrency: concurrency})
	finish := func(ctx workflow.Context, i int) {
		call := calls[i]
		def, _ := registry.Lookup(call.Name)
		data := events.ToolData{ToolCallID: call.ID, Name: call.Name, Args: call.Args, ExecutionMode: string(def.ExecutionMode), Details: map[string]string{"replay": string(def.Replay)}}
		_ = r.emit(ctx, events.ToolStart, events.Parallel, data)
		if allowed[i] {
			batch.Results[i] = r.executeAllowed(ctx, input(call), def.Replay)
		}
		result := batch.Results[i]
		if call.Name == "write_file" && !result.IsError && !result.Interrupted {
			r.state.WroteFile = true
		}
		r.removeActiveTool(call.ID)
		data.IsError = result.IsError
		data.Interrupted = result.Interrupted
		data.Summary = result.Output
		for k, v := range result.Details {
			data.Details[k] = v
		}
		_ = r.emit(ctx, events.ToolEnd, events.Parallel, data)
	}
	if sequential {
		for i := range calls {
			finish(ctx, i)
		}
	} else {
		wg := workflow.NewWaitGroup(ctx)
		sem := workflow.NewSemaphore(ctx, 4)
		for i := range calls {
			wg.Add(1)
			workflow.Go(ctx, func(ctx workflow.Context) {
				defer wg.Done()
				if err := sem.Acquire(ctx, 1); err != nil {
					batch.Results[i].Output = bounded(err.Error(), 8192)
					r.removeActiveTool(calls[i].ID)
					return
				}
				defer sem.Release(1)
				finish(ctx, i)
			})
		}
		wg.Wait(ctx)
	}
	_ = r.emit(ctx, events.ParallelEnd, events.Parallel, events.ParallelData{ToolCallIDs: ids})
	batch.LastSeq = r.state.LastSeq
	return batch, nil
}

func (r *runtime) removeActiveTool(id string) {
	for i, call := range r.state.ActiveTools {
		if call.ID == id {
			// ActiveTools aliases the parent's model calls and execution batch.
			// Never shift that storage: allowed[i] must stay bound to calls[i].
			remaining := make([]llm.ToolCall, 0, len(r.state.ActiveTools)-1)
			remaining = append(remaining, r.state.ActiveTools[:i]...)
			r.state.ActiveTools = append(remaining, r.state.ActiveTools[i+1:]...)
			return
		}
	}
}

func (r *runtime) executeAllowed(ctx workflow.Context, in ToolInput, replay tools.Replay) tools.Result {
	if in.Call.Name == "delegate" {
		return r.delegate(ctx, in.Call)
	}
	attempts := int32(3)
	if replay == tools.Unsafe {
		attempts = 1
	}
	opts := workflow.ActivityOptions{TaskQueue: AgentsQueue, StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: attempts}, WaitForCancellation: true}
	result := tools.Result{ToolCallID: in.Call.ID}
	var err error
	if in.Call.Name == "run_command" {
		opts.StartToCloseTimeout = 30 * time.Minute
		opts.HeartbeatTimeout = 10 * time.Second
		var command CommandResult
		err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), CommandActivity, in).Get(ctx, &command)
		if err != nil && (temporal.IsTimeoutError(err) || temporal.IsCanceledError(err)) {
			recoveryCtx, cancel := workflow.NewDisconnectedContext(ctx)
			_ = workflow.ExecuteActivity(activityContext(recoveryCtx), RecoverCommandActivity, in).Get(recoveryCtx, &command)
			cancel()
			command.Interrupted = true
		} else if err != nil {
			// Failed activities do not deliver their normal result. RunCommand
			// preserves its tail/log reference in application-error details instead.
			var appErr *temporal.ApplicationError
			var details CommandResult
			if errors.As(err, &appErr) && appErr.HasDetails() && appErr.Details(&details) == nil {
				command = details
			}
		}
		result.Output = command.Tail
		result.Interrupted = command.Interrupted
		result.IsError = command.ExitCode != 0 || command.Interrupted
		result.Details = map[string]string{"exitCode": fmt.Sprint(command.ExitCode), "logRef": command.LogRef, "truncated": fmt.Sprint(command.Truncated)}
	} else {
		err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), ToolActivity, in).Get(ctx, &result)
	}
	result.ToolCallID = in.Call.ID
	if err != nil {
		result.IsError = true
		if replay == tools.Unsafe && (temporal.IsTimeoutError(err) || temporal.IsCanceledError(err)) {
			result.Interrupted = true
		}
		if result.Output == "" {
			result.Output = err.Error()
		}
	}
	result.Output = bounded(result.Output, 8192)
	return result
}

func (r *runtime) gatherContext(ctx workflow.Context) error {
	if r.state.ContextGathered {
		return nil
	}
	kinds := []string{"git", "tree", "readme", "todo"}
	futures := make([]workflow.Future, len(kinds))
	_ = r.emit(ctx, events.ParallelStart, events.Parallel, events.ParallelData{ToolCallIDs: kinds, Concurrency: 4})
	for i, kind := range kinds {
		futures[i] = workflow.ExecuteActivity(activityContext(ctx), GatherActivity, GatherInput{Scope: r.scope, DataDir: r.cfg.DataDir, Workdir: r.cfg.Workdir, Kind: kind})
	}
	var firstErr error
	for _, future := range futures {
		var result AppendResult
		if err := future.Get(ctx, &result); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		r.recordSeq(result.LastSeq)
		if r.state.TranscriptRef != "" && r.state.TranscriptRef != result.TranscriptRef {
			if firstErr == nil {
				firstErr = fmt.Errorf("context transcript mismatch")
			}
		} else {
			r.state.TranscriptRef = result.TranscriptRef
		}
	}
	_ = r.emit(ctx, events.ParallelEnd, events.Parallel, events.ParallelData{ToolCallIDs: kinds})
	if firstErr == nil {
		r.state.ContextGathered = true
	}
	return firstErr
}
