package workflows

import "go.temporal.io/sdk/workflow"

// afterRun runs before agent_end/agent_settled while Status is still working.
func (r *runtime) afterRun(workflow.Context) error { return nil }
