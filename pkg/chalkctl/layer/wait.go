package layer

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WaitFor runs check every interval until it passes, up to timeout. A check that fails with a
// Permanent error ends the wait at once with the error it wraps. Once the time is up, the error
// is the last check's; once ctx ended, it is the context's cause. A what other than "" names what
// was waited for in either: "waiting for <what>: ...".
func WaitFor(ctx context.Context, what string, timeout, interval time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		var p Permanent
		switch {
		case err == nil:
			return nil
		case errors.As(err, &p):
			return p.Err
		case time.Now().Before(deadline):
			select {
			case <-ctx.Done():
				err = context.Cause(ctx)
			case <-time.After(interval):
				continue
			}
		}
		if what == "" {
			return err
		}
		return fmt.Errorf("waiting for %s: %w", what, err)
	}
}

// Permanent is a check's error that waiting does not change.
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }

func (p Permanent) Unwrap() error { return p.Err }

// Count is n and the noun, in the plural unless n is one.
func Count[N ~int | ~uint32 | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
