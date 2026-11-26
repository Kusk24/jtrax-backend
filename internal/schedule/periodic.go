// Package schedule runs periodic work inside the school server.
package schedule

import (
	"context"
	"time"
)

// Start runs a job immediately and then on each tick. A slow run finishes
// before the same job can run again. Cancellation stops future ticks.
func Start(ctx context.Context, interval time.Duration, run func()) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			run()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
