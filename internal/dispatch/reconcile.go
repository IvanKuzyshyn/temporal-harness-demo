package dispatch

import (
	"context"
	"errors"
	"time"
)

// ErrRepositoryUnavailable is the sanitized supervisor signal after the retry
// budget is exhausted. It deliberately does not wrap private SQL/connection errors.
var ErrRepositoryUnavailable = errors.New("dispatcher repository unavailable")

// Run reconciles both never-dispatched and dispatched-but-pending commands.
// Three repository failures without a successful worked cycle return
// ErrRepositoryUnavailable. Each retry waits PollInterval. Empty polls do not
// reset the budget: failed acknowledgements may leave commands hidden by leases.
// The caller must observe the return, mark reconciliation unhealthy, and back off
// before restarting. Transport connectivity alone is not repository readiness.
// A bounded burst avoids idle latency without spinning on a service outage.
// Gateway implementations must honor the supplied context; forcibly abandoning
// arbitrary Go code would leak goroutines and cannot guarantee cancellation.
func (d *Dispatcher) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		for n := 0; n < 32; n++ {
			worked, err := d.RunOnce(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				failures++
				if failures >= 3 {
					return ErrRepositoryUnavailable
				}
				break
			}
			if !worked {
				break
			}
			failures = 0
		}
		timer.Reset(d.opts.PollInterval)
	}
}
