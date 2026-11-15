// Package renewaltest runs a Renewal in a test's synctest bubble, where the waits between its
// checks pass in fake time, and hands the test each check.
package renewaltest

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/renewal"
)

// Run runs r until the test ends and returns a function that waits for r's next check and
// returns the time it checked at, once the check and any renewal it started have ended. The
// time between two checks is the wait r chose. Call both in a synctest bubble.
func Run(t *testing.T, r *renewal.Renewal) (next func() time.Time) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	checks := make(chan time.Time)
	due := r.Due
	r.Due = func(now time.Time) (bool, time.Time, error) {
		select {
		case checks <- now:
		case <-ctx.Done():
		}
		return due(now)
	}
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return func() time.Time {
		at := <-checks
		synctest.Wait()
		return at
	}
}
