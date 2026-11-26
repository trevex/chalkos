package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
)

// changeWait is how long Reboot and Shutdown wait for a change of the node to end before they
// refuse.
const changeWait = 5 * time.Second

// changeHolder names the change that holds one of the locks of the node's changes, for the
// refusals of Reboot, Shutdown and Drain.
type changeHolder struct{ what atomic.Pointer[string] }

// hold names what as the lock's holder until the returned func is called, which the holder defers
// once it took the lock.
func (h *changeHolder) hold(what string) (release func()) {
	h.what.Store(&what)
	return func() { h.what.Store(nil) }
}

// runningChange names the change that holds s.upgrading or s.mu, as far as it named itself.
func (s *Server) runningChange() string {
	for _, h := range []*changeHolder{&s.upgradingBy, &s.muBy} {
		if what := h.what.Load(); what != nil {
			return *what
		}
	}
	return "a change of the node"
}

// errGoingDown is the refusal of lockChanges once the node goes down.
var errGoingDown = errors.New("the node goes down")

// lockChanges takes s.upgrading and s.mu once no change of the node holds them, trying until ctx
// ends or the node goes down. It only tries the locks, as handlers that hold s.mu try
// s.upgrading, so it cannot deadlock with them.
func (s *Server) lockChanges(ctx context.Context) (unlock func(), err error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.goingDown.Load() {
			return nil, errGoingDown
		}
		if s.upgrading.TryLock() {
			if s.mu.TryLock() {
				return func() {
					s.mu.Unlock()
					s.upgrading.Unlock()
				}, nil
			}
			s.upgrading.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// afterChanges calls f, which starts to reboot or shut down the node (verb), once the response
// has had time to reach the client and no change of the node runs. It waits up to changeWait for
// the changes' locks and keeps them once f started, so no change starts before the node goes
// down; f's failure releases them. A change that runs longer refuses the call with ABORTED.
// force calls f without waiting, which interrupts the change.
func (s *Server) afterChanges(ctx context.Context, force bool, verb string, f func() error) error {
	if force {
		s.afterResponse(func() { goDown(verb, f) })
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, changeWait)
	defer cancel()
	unlock, err := s.lockChanges(wait)
	switch {
	case errors.Is(err, errGoingDown):
		return layer.Errorf(connect.CodeAborted, "the node goes down already")
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return layer.Errorf(connect.CodeAborted, "%s is running; %s once it ended", s.runningChange(), verb)
	}
	s.afterResponse(func() {
		if goDown(verb, f) != nil {
			unlock()
			return
		}
		s.goingDown.Store(true)
	})
	return nil
}

// goDown calls f, which starts to reboot or shut down the node (verb), and logs its failure.
func goDown(verb string, f func() error) error {
	err := f()
	if err != nil {
		log.Printf("%s: %v", verb, err)
	}
	return err
}

// Drain waits, until ctx ends, for the changes of the node that run. chalkd drains once it serves
// no more calls, so what may start a change then, the trial's deadline and the layers' loops, ends
// with chalkd's context; holding the locks would keep the loops from ending. Once the node goes
// down, a reboot or shutdown holds the locks and no change runs.
func (s *Server) Drain(ctx context.Context) error {
	unlock, err := s.lockChanges(ctx)
	if errors.Is(err, errGoingDown) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s still runs", s.runningChange())
	}
	unlock()
	return nil
}
