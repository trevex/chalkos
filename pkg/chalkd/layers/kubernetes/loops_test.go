package kubernetes

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoopGroupStop ends the group's context, waits for the loops that end and gives up on one
// that does not after the timeout.
func TestLoopGroupStop(t *testing.T) {
	g := newLoopGroup()
	var ended atomic.Bool
	g.Go("a loop that ends", func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		ended.Store(true)
	})
	release := make(chan struct{})
	defer close(release)
	g.Go("a loop that does not end", func(context.Context) { <-release })
	start := time.Now()
	g.Stop(100 * time.Millisecond)
	if !ended.Load() {
		t.Error("Stop returned before the loop that ends ended")
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Errorf("Stop returned after %v, before the timeout of the loop that does not end", d)
	}
}
