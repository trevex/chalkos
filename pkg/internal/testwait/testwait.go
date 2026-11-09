// Package testwait waits in a test for a condition that another goroutine brings about.
package testwait

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/internal/poll"
)

// Eventually checks every 20ms until check holds, failing the test when it does not hold within
// a minute.
func Eventually(t testing.TB, what string, check func() bool) {
	t.Helper()
	err := poll.Until(context.Background(), what, time.Minute, 20*time.Millisecond, func() error {
		if check() {
			return nil
		}
		return errors.New("it does not hold")
	})
	if err != nil {
		t.Fatal(err)
	}
}
