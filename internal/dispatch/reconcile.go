package dispatch

import (
	"context"
	"time"
)

// Run reconciles both never-dispatched and dispatched-but-pending commands.
// A bounded burst avoids idle latency without spinning on a service outage.
// Gateway implementations must honor the supplied context; forcibly abandoning
// arbitrary Go code would leak goroutines and cannot guarantee cancellation.
func (d *Dispatcher) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
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
			if err != nil || !worked {
				break
			}
		}
		timer.Reset(d.opts.PollInterval)
	}
}
