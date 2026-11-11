package workflows

import "go.temporal.io/sdk/workflow"

// Observation-only boundaries. workflow.Now is deterministic and adds no command
// to history; old fixtures retain their scheduling and activity inputs.
func (r *runtime) phase(ctx workflow.Context, phase string) {
	if r.state.Phase != phase {
		r.state.Phase = phase
		r.state.PhaseStartedAt = workflow.Now(ctx)
	}
}
