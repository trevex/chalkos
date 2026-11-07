package poll

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUntilPasses(t *testing.T) {
	n := 0
	err := Until(context.Background(), "it", time.Second, time.Millisecond, func() error {
		if n++; n < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Errorf("err = %v after %d checks, want success after 3", err, n)
	}
}

// Once the time is up the error is the last check's, naming what was waited for.
func TestUntilTimesOut(t *testing.T) {
	n := 0
	err := Until(context.Background(), "the disk", 30*time.Millisecond, 10*time.Millisecond, func() error {
		n++
		return errors.New("busy")
	})
	if err == nil || err.Error() != "waiting for the disk: busy" || n < 2 {
		t.Errorf("err = %v after %d checks", err, n)
	}
}

// A negative timeout, a time already past, checks once; without what the error is the check's.
func TestUntilPastChecksOnce(t *testing.T) {
	n := 0
	busy := errors.New("busy")
	err := Until(context.Background(), "", -time.Second, time.Hour, func() error {
		n++
		return busy
	})
	if err != busy || n != 1 {
		t.Errorf("err = %v after %d checks, want the check's error after one", err, n)
	}
}

// Without a timeout the wait ends with ctx, keeping the last check's error and the cause.
func TestUntilContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := Until(ctx, "etcd to stop", 0, 10*time.Millisecond, func() error { return errors.New("it listens") })
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "waiting for etcd to stop: it listens: context deadline exceeded" {
		t.Errorf("err = %v", err)
	}
}

// A check whose error carries the context's end already is not followed by it again.
func TestUntilContextEndsInTheCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Until(ctx, "", 0, time.Hour, func() error { return ctx.Err() })
	if err != context.Canceled {
		t.Errorf("err = %v, want the check's", err)
	}
}

// A permanent error ends the wait at once with the error it wraps.
func TestUntilPermanent(t *testing.T) {
	n := 0
	refused := errors.New("refused")
	err := Until(context.Background(), "it", time.Hour, time.Millisecond, func() error {
		n++
		return Permanent{Err: refused}
	})
	if err != refused || n != 1 {
		t.Errorf("err = %v after %d checks, want the wrapped error after one", err, n)
	}
}
