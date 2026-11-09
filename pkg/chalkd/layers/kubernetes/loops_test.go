package kubernetes

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
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

// A join of loops that stopped leaves the join of the loops started since alone as it ends.
func TestLeftoverJoinLeavesTheNextGeneration(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	// The join ends at once: the node is bootstrapped.
	write(t, k.paths.Bootstrapped(), "")
	k.mu.Lock()
	old := k.loops()
	k.joining = true
	k.mu.Unlock()
	k.stopLoops()
	k.mu.Lock()
	next := k.loops()
	k.joining = true
	k.mu.Unlock()
	t.Cleanup(k.stopLoops)
	k.superviseJoin(old.ctx, old)
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.joining || k.group != next {
		t.Errorf("the leftover join changed the next generation: joining %v, its group kept %v", k.joining, k.group == next)
	}
}
