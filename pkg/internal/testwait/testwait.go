// Package testwait waits in a test for something that another goroutine brings about.
//
// Eventually waits in real time for a condition. It suits tests that talk to the outside of the
// process or over real I/O (servers, sockets, child processes), where nothing can fake the clock.
// Bubble runs a test in a synctest bubble with a fake clock. It suits tests whose waits are all
// in-process (timers, channels, polling loops): there a sleep stands for time passing and costs no
// wall time, and synctest.Wait waits until every other goroutine of the bubble is blocked.
package testwait

import (
	"testing"
	"testing/synctest"
	"time"
)

// Eventually checks until check holds, failing the test when it does not hold within a minute.
// It looks again after a millisecond and then twice as long after each look, up to 100ms.
func Eventually(t testing.TB, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for pause := time.Millisecond; !check(); pause = min(2*pause, 100*time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: it does not hold after a minute", what)
		}
		time.Sleep(pause)
	}
}

// Bubble runs f in a synctest bubble whose clock starts at the real time, so certificates the
// test issues or reads are valid. In it, waits on time end without waiting in real time.
// f must not call t.Run or t.Parallel; a test with subtests bubbles each subtest's body.
func Bubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	now := time.Now()
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(now))
		f(t)
	})
}
