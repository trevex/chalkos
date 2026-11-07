// Package poll waits for a condition by checking it again and again.
package poll

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Until runs check every interval until it passes, up to timeout: a timeout of 0 waits until ctx
// ends, and a negative one, a time already past, checks once. A check that fails with a Permanent
// error ends the wait at once with the error it wraps. Otherwise a wait that ends before a check
// passed returns the last check's error, followed by the context's cause when ctx ended the wait
// and the check's error does not say so already. A what other than "" names what was waited for:
// "waiting for <what>: ...".
func Until(ctx context.Context, what string, timeout, interval time.Duration, check func() error) error {
	var deadline time.Time
	if timeout != 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		err := check()
		if err == nil {
			return nil
		}
		if p, ok := errors.AsType[Permanent](err); ok {
			return p.Err
		}
		wait := interval
		if !deadline.IsZero() {
			left := time.Until(deadline)
			if left <= 0 {
				return named(what, err)
			}
			wait = min(wait, left)
		}
		select {
		case <-ctx.Done():
			if cause := context.Cause(ctx); !errors.Is(err, cause) {
				err = fmt.Errorf("%w: %w", err, cause)
			}
			return named(what, err)
		case <-time.After(wait):
		}
	}
}

func named(what string, err error) error {
	if what == "" {
		return err
	}
	return fmt.Errorf("waiting for %s: %w", what, err)
}

// Permanent is a check's error that waiting does not change.
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }

func (p Permanent) Unwrap() error { return p.Err }
