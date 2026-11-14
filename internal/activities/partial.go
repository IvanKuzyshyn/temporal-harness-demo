package activities

import (
	"context"
	"sync"
	"time"
)

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
