package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"go.temporal.io/sdk/workflow"
)

// SessionWorkflow stays queryable after settlement, until global cancellation.
func SessionWorkflow(ctx workflow.Context, input Input) error {
	if input.MaxTurns <= 0 {
		input.MaxTurns = 12
	}
	if input.ContinueAfterTurns <= 0 {
		input.ContinueAfterTurns = 100
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
	if err := validateRestartCount(r.state.AfterRunCount); err != nil {
		return err
	}
	r.cfg.Carry = nil // Never nest a previous carry into the next execution.
	r.executionStartTurns = r.state.TotalTurns
	r.pruneChildren()
	if err := workflow.SetQueryHandler(ctx, StateQuery, func() (State, error) { return r.queryState(), nil }); err != nil {
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
		r.deferringSteers = false
		r.steerCounter = 0
		r.state.Status = "working"
		r.state.RunCounter++
		r.state.Turn = 0
		r.state.Answer = ""
		r.state.LastRunFailure = nil
		r.state.WroteFile = false
		r.inputID = runInputID(r.state.RunCounter, msg.RequestID)
		if !msg.Internal {
			r.state.AfterRunCount = 0
			r.state.VerificationPending = false
			r.state.OriginInputID = r.inputID
		} else if msg.OriginInputID != "" {
			r.state.OriginInputID = msg.OriginInputID
			r.state.AfterRunCount = msg.AfterRunCount
			r.state.VerificationPending = msg.VerificationPending
		}
		err = r.run(runCtx, msg)
		if r.toolPersistenceErr != nil {
			cancel()
			return r.failToolPersistence()
		}
		r.deferringSteers = true
		if runCtx.Err() != nil {
			r.fail("aborted", runCtx.Err())
		} else if err != nil && r.state.LastRunFailure == nil {
			r.fail("interrupted", err)
		}
		// Failures never trigger afterRun. Late steers are now next-run user work.
		if r.state.LastRunFailure == nil {
			if err = r.afterRun(runCtx); err != nil && r.state.LastRunFailure == nil {
				reason := "interrupted"
				if runCtx.Err() != nil {
					reason = "aborted"
				}
				r.fail(reason, err)
			}
		}
		// afterRun may also persist joined results. Fatal persistence takes
		// precedence over cancellation and must never enter normal settlement.
		if r.toolPersistenceErr != nil {
			cancel()
			return r.failToolPersistence()
		}
		if runCtx.Err() != nil {
			r.fail("aborted", runCtx.Err())
		}
		cancel()
		// Keep cancel nonnil through settlement: Continue must reject this boundary.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.state.LastRunFailure != nil {
			r.state.QueuePaused = true
			// Retained inputs become FIFO next-run work on resume, even if originally steers.
			for i := range r.state.Inbox {
				r.state.Inbox[i].WhenBusy = ""
			}
		}
		r.pruneChildren()
		r.settle(ctx)
		r.cancel = nil
	}
}

// failToolPersistence projects terminal state without claiming the unresolved
// transcript tail was settled. Both joined run seams use the same fatal error.
func (r *runtime) failToolPersistence() error {
	r.fail("interrupted", r.toolPersistenceErr)
	r.state.Status = "failed"
	r.state.QueuePaused = true
	r.state.ActiveTools = nil
	r.state.PendingApproval = nil
	return r.toolPersistenceErr
}

func (r *runtime) settle(ctx workflow.Context) {
	status := "done"
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
	if err := r.emit(ctx, events.AgentEnd, events.None, data); err != nil {
		workflow.GetLogger(ctx).Warn("agent_end emission failed", "error", err)
	}
	// A queued follow-up starts after agent_end. A failed queue is paused, not
	// automatic work, and therefore does settle. Updates during agent_end count.
	if !r.automaticWork() {
		if err := r.emit(ctx, events.AgentSettled, events.None, data); err != nil {
			workflow.GetLogger(ctx).Warn("agent_settled emission failed", "error", err)
		}
	}
	r.state.Status = "idle"
}
