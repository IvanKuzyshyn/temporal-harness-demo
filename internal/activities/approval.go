package activities

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// NotifyApproval is only a best-effort display sidechannel, never policy.
// Desktop notifications are opt-in and contain no tool arguments or secrets.
func (a *Activities) NotifyApproval(ctx context.Context, in workflows.EmitInput) error {
	return a.notifyApproval(ctx, in, os.Stderr, os.Getenv("HARNESS_DESKTOP_NOTIFY") == "1", runtime.GOOS, exec.LookPath, func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = 100 * time.Millisecond
		return cmd.Run()
	})
}

func (a *Activities) notifyApproval(ctx context.Context, in workflows.EmitInput, stderr io.Writer, desktop bool, platform string, lookup func(string) (string, error), run func(context.Context, string, ...string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Do not turn a missing/full display store or failed bell into a policy error.
	_, _ = a.EmitEvent(ctx, in)
	_, _ = fmt.Fprint(stderr, "\aHarness: approval requested; review the pending action.\n")
	if !desktop {
		return ctx.Err()
	}
	var name string
	var args []string
	switch platform {
	case "darwin":
		name = "osascript"
		args = []string{"-e", `display notification "Review the pending action in Harness" with title "Approval requested"`}
	case "linux":
		name = "notify-send"
		args = []string{"--", "Harness approval requested", "Review the pending action in Harness"}
	default:
		return ctx.Err()
	}
	path, err := lookup(name)
	if err != nil {
		return ctx.Err()
	}
	notifyCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_ = run(notifyCtx, path, args...)
	return ctx.Err()
}
