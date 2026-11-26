package schedule

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestStartsImmediatelyAndStopsOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runs := 0
		Start(ctx, time.Minute, func() { runs++ })
		synctest.Wait()
		if runs != 1 {
			t.Fatalf("startup calls = %d, want 1", runs)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if runs != 2 {
			t.Fatalf("calls after one interval = %d, want 2", runs)
		}
		cancel()
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		if runs != 2 {
			t.Fatalf("calls after cancellation = %d, want 2", runs)
		}
	})
}
