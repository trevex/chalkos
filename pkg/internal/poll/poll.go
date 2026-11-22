// Package poll waits for a condition by checking it again and again.
package poll

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Until runs check every interval until it passes, up to timeout; a timeout of 0 or less checks
// once. Each check gets a context that ends at the timeout, so a check stuck in a call cannot
// overrun it; the single check of a timeout already past gets ctx. A Permanent error ends the wait
// at once. A failed wait returns the last check's error, or the one before when the timeout cut the
// last check short, with ctx's cause when ctx ended it, prefixed "waiting for <what>: " when what
// is not empty.
func Until(ctx context.Context, what string, timeout, interval time.Duration, check func(context.Context) error) error {
	return wait(ctx, what, time.Now().Add(timeout), interval, check)
}

// UntilDone is Until bounded by ctx alone: it checks until a check passes or ctx ends.
func UntilDone(ctx context.Context, what string, interval time.Duration, check func(context.Context) error) error {
	return wait(ctx, what, time.Time{}, interval, check)
}

// wait checks until deadline, or until ctx ends when deadline is zero.
func wait(ctx context.Context, what string, deadline time.Time, interval time.Duration, check func(context.Context) error) error {
	checkCtx := ctx
	if !deadline.IsZero() && time.Until(deadline) > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	var prev error
	for {
		err := check(checkCtx)
		if err == nil {
			return nil
		}
		// A check the timeout cut short says less than the one before it.
		if prev != nil && checkCtx.Err() != nil && ctx.Err() == nil {
			return named(what, prev)
		}
		if p, ok := errors.AsType[Permanent](err); ok {
			return p.Err
		}
		pause := interval
		if !deadline.IsZero() {
			left := time.Until(deadline)
			if left <= 0 {
				return named(what, err)
			}
			prev = err
			pause = min(pause, left)
		}
		select {
		case <-ctx.Done():
			if cause := context.Cause(ctx); !errors.Is(err, cause) {
				err = fmt.Errorf("%w: %w", err, cause)
			}
			return named(what, err)
		case <-time.After(pause):
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

// Sustained is a condition a wait finds again and again, which counts once it has held for
// Window without a look finding it otherwise.
type Sustained struct {
	Window time.Duration
	since  time.Time
}

// Holds records that a look found the condition, and reports whether it has held for the window
// since the first look that found it.
func (s *Sustained) Holds() bool {
	now := time.Now()
	if s.since.IsZero() {
		s.since = now
	}
	return now.Sub(s.since) >= s.Window
}

// Reset records that a look found the condition no longer holds.
func (s *Sustained) Reset() { s.since = time.Time{} }
