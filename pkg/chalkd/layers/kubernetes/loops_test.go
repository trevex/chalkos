package kubernetes

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/internal/testwait"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// TestLoopGroupStop ends the group's context, waits for the loops that end and gives up on those
// that do not once the timeout, one for all of them, passed.
func TestLoopGroupStop(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		g := newLoopGroup()
		var ended atomic.Bool
		g.Go("a loop that ends", func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			ended.Store(true)
		})
		release := make(chan struct{})
		defer close(release)
		for range 3 {
			g.Go("a loop that does not end", func(context.Context) { <-release })
		}
		start := time.Now()
		g.Stop(context.Background(), 100*time.Millisecond)
		if !ended.Load() {
			t.Error("Stop returned before the loop that ends ended")
		}
		if d := time.Since(start); d != 100*time.Millisecond {
			t.Errorf("Stop returned after %v, want the timeout of 100ms", d)
		}
	})
}

// A context that ends before the timeout ends Stop's wait, as the restart action's does.
func TestLoopGroupStopEndsWithItsContext(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		g := newLoopGroup()
		release := make(chan struct{})
		defer close(release)
		g.Go("a loop that does not end", func(context.Context) { <-release })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		g.Stop(ctx, time.Minute)
		if d := time.Since(start); d != 20*time.Millisecond {
			t.Errorf("Stop returned after %v, want the context's 20ms", d)
		}
	})
}

// A join of loops that stopped leaves the join of the loops started since alone as it ends.
func TestLeftoverJoinLeavesTheNextGeneration(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	// The join ends at once: the node is bootstrapped.
	write(t, k.state.BootstrappedMarker(), "")
	k.mu.Lock()
	old := k.loops()
	k.joining = true
	k.mu.Unlock()
	k.stopLoops(context.Background())
	k.mu.Lock()
	next := k.loops()
	k.joining = true
	k.mu.Unlock()
	t.Cleanup(func() { k.stopLoops(context.Background()) })
	k.superviseJoin(old.ctx, old)
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.joining || k.group != next {
		t.Errorf("the leftover join changed the next generation: joining %v, its group kept %v", k.joining, k.group == next)
	}
}
