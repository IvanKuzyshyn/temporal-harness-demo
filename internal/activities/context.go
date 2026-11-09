package activities

import (
	"context"
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func (a *Activities) GatherContext(context.Context, workflows.GatherInput) (workflows.AppendResult, error) {
	return workflows.AppendResult{}, fmt.Errorf("context gathering is not supported in the basic session")
}
