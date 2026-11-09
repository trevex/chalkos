package kubernetes

import (
	"context"
	"log"
	"sync"
	"time"
)

// loopGroup runs loops that end together: each runs in the group's context until Stop ends it.
type loopGroup struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	loops []loop
}

// loop is a loop of a group, named for the log, and the channel closed once it ended.
type loop struct {
	name string
	done chan struct{}
}

func newLoopGroup() *loopGroup {
	ctx, cancel := context.WithCancel(context.Background())
	return &loopGroup{ctx: ctx, cancel: cancel}
}

// Go runs f in the group's context, as the loop name.
func (g *loopGroup) Go(name string, f func(ctx context.Context)) {
	done := make(chan struct{})
	g.mu.Lock()
	g.loops = append(g.loops, loop{name: name, done: done})
	g.mu.Unlock()
	go func() {
		defer close(done)
		f(g.ctx)
	}()
}

// Stop ends the group's context and waits for each loop, in the order they started, up to timeout
// for each. It logs the loops that did not end in time.
func (g *loopGroup) Stop(timeout time.Duration) {
	g.cancel()
	g.mu.Lock()
	loops := g.loops
	g.mu.Unlock()
	for _, l := range loops {
		select {
		case <-l.done:
		case <-time.After(timeout):
			log.Printf("kubernetes: %s did not end in time", l.name)
		}
	}
}
