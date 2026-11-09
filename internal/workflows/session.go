package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"go.temporal.io/sdk/workflow"
)

// SessionWorkflow deliberately does not return after a run settles. Its Query
// remains authoritative and new Updates can reconnect to the same execution.
func SessionWorkflow(ctx workflow.Context, input Input) error {
	if input.MaxTurns <= 0 {
		input.MaxTurns = 12
	}
	if input.DataDir == "" {
		input.DataDir = ".data"
	}
	if input.Model == "" {
		input.Model = "gemma4:e4b"
	}
	info := workflow.GetInfo(ctx)
	r := &runtime{cfg: input, state: State{Status: "idle", MaxTurns: input.MaxTurns}, scope: events.Scope{StreamID: info.WorkflowExecution.ID, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID}}
	if input.Carry != nil {
		r.state = *input.Carry
		r.state.Status = "idle"
		r.state.MaxTurns = input.MaxTurns
	}
	if err := workflow.SetQueryHandler(ctx, StateQuery, func() (State, error) { return r.state, nil }); err != nil {
		return err
	}
	if err := r.registerInboxHandlers(ctx); err != nil {
		return err
	}
	if err := r.registerApprovalHandlers(ctx); err != nil {
		return err
	}
	for {
		if err := r.continueAsNew(ctx); err != nil {
			return err
		}
		msg, err := r.nextInput(ctx)
		if err != nil {
			return err
		}
		runCtx, cancel := workflow.WithCancel(ctx)
		r.cancel = cancel
		r.state.Status = "working"
		r.state.RunCounter++
		r.state.Turn = 0
		r.state.Answer = ""
		r.state.LastRunFailure = nil
		r.state.AfterRunCount = 0
		r.inputID = runInputID(r.state.RunCounter, msg.RequestID)
		err = r.run(runCtx, msg)
		if err != nil && r.state.LastRunFailure == nil {
			r.fail("interrupted", err)
		}
		// afterRun is part of working, not an already-settled background operation.
		if err = r.afterRun(runCtx); err != nil {
			r.fail("interrupted", err)
		}
		cancel()
		r.cancel = nil
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.settle(ctx)
	}
}

func (r *runtime) settle(ctx workflow.Context) {
	status := "done" // Event wire status differs from authoritative State.Status.
	r.state.Status = "settled"
	if r.state.LastRunFailure != nil {
		status = "failed"
		r.state.Status = "failed"
		_ = r.emit(ctx, events.RunFailed, events.None, r.state.LastRunFailure)
	}
	r.state.ActiveTools = nil
	r.state.PendingApproval = nil
	data := events.AgentData{InputID: r.inputID, Status: status, Reason: "done"}
	if r.state.LastRunFailure != nil {
		data.RunFailure = r.state.LastRunFailure.Reason
		data.Reason = r.state.LastRunFailure.Reason
	}
	// Display failures must not make an otherwise durable session disappear.
	if err := r.emit(ctx, events.AgentEnd, events.None, data); err != nil {
		workflow.GetLogger(ctx).Warn("agent_end emission failed", "error", err)
	}
	if err := r.emit(ctx, events.AgentSettled, events.None, data); err != nil {
		workflow.GetLogger(ctx).Warn("agent_settled emission failed", "error", err)
	}
	r.state.Status = "idle"
}
