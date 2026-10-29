// Package renewaltest is the fake clock that drives a Renewal in the tests of chalkd and of the
// renewal package, so a test ends each wait itself instead of waiting on it in real time.
package renewaltest

import (
	"context"
	"testing"
	"time"
)

// Clock hands the waits a Renewal asks for to the test, which ends each one with Next or Advance.
type Clock struct {
	now   time.Time
	waits chan time.Duration
	fire  chan time.Time
}

// NewClock returns a clock at now.
func NewClock(now time.Time) *Clock {
	return &Clock{now: now, waits: make(chan time.Duration), fire: make(chan time.Time)}
}

// Now is the clock's current time, for a Renewal's Now.
func (c *Clock) Now() time.Time { return c.now }

// After hands the wait to the test and returns the channel Next or Advance fires, for a
// Renewal's After.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.waits <- d
	return c.fire
}

// Waits is the channel each wait's duration arrives on, for a select of the test's own.
func (c *Clock) Waits() <-chan time.Duration { return c.waits }

// Next returns the wait the renewal asked for, or fails the test if none comes within a minute.
func (c *Clock) Next(t testing.TB) time.Duration {
	t.Helper()
	select {
	case d := <-c.waits:
		return d
	case <-time.After(time.Minute):
		t.Fatal("the renewal asked for no wait")
		return 0
	}
}

// Advance sets the clock to now, ends the wait it is handing out and returns the next one.
func (c *Clock) Advance(t testing.TB, now time.Time) time.Duration {
	t.Helper()
	c.now = now
	c.fire <- now
	return c.Next(t)
}

// Drain fires the clock until done closes, so a renewal still ending after the test cancelled
// its context finds a clock to receive from rather than blocking on one nobody reads any more.
func (c *Clock) Drain(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-c.waits:
		case c.fire <- c.now:
		}
	}
}

// Run starts run, such as a Renewal's Run, against clock and arranges for it to stop once the
// test ends. It returns a function that ends the current wait at the time given and returns the
// next one.
func Run(t testing.TB, run func(context.Context), clock *Clock) func(time.Time) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		clock.Drain(done)
	})
	clock.Next(t)
	return func(now time.Time) time.Duration { return clock.Advance(t, now) }
}
