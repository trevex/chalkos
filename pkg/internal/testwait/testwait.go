// Package testwait waits in a test for a condition that another goroutine brings about.
package testwait

import (
	"testing"
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
