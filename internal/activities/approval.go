package activities

import (
	"context"
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func (a *Activities) NotifyApproval(context.Context, workflows.EmitInput) error {
	return fmt.Errorf("approval notification is not supported in the basic session")
}
