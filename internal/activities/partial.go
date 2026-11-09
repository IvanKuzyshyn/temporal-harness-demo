package activities

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
)

type streamProgress struct {
	Tokens     int    `json:"tokens"`
	PartialRef string `json:"partialRef"`
}

// Each invocation owns a separate file, including overlapping retries. The
// opaque key includes run/input/turn identity; no user-controlled path survives.
func openPartial(ctx context.Context, in workflows.LLMInput, id string) (*os.File, string, error) {
	safe := fmt.Sprintf("%x", sha256.Sum256([]byte(in.Scope.WorkflowID)))
	dir := filepath.Join(in.DataDir, "artifacts", safe)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", err
	}
	attempt := int32(1)
	if activity.IsActivity(ctx) {
		attempt = activity.GetInfo(ctx).Attempt
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf("turn-%s-%d-attempt-%d-*.partial", id, in.Turn, attempt))
	if err != nil {
		return nil, "", err
	}
	ref, err := filepath.Rel(in.DataDir, f.Name())
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, filepath.ToSlash(ref), nil
}

// Joined on every return path; context cancellation does not outlive cleanup.
func startStreamHeartbeats(ctx context.Context, beat func()) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		beat()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				beat()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }); <-exited }
}
