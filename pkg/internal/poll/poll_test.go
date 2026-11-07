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

// A timeout of 0 or less, a time already past, checks once; without what the error is the check's.
func TestUntilPastChecksOnce(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		n := 0
		busy := errors.New("busy")
		err := Until(context.Background(), "", timeout, time.Hour, func() error {
			n++
			return busy
		})
		if err != busy || n != 1 {
			t.Errorf("timeout %v: err = %v after %d checks, want the check's error after one", timeout, err, n)
		}
	}
}

// A context that ends before the timeout ends the wait, keeping the last check's error and the
// cause.
func TestUntilContextEndsFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := Until(ctx, "the disk", time.Hour, 10*time.Millisecond, func() error { return errors.New("busy") })
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "waiting for the disk: busy: context deadline exceeded" {
		t.Errorf("err = %v", err)
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

func TestUntilDonePasses(t *testing.T) {
	n := 0
	err := UntilDone(context.Background(), "it", time.Millisecond, func() error {
		if n++; n < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Errorf("err = %v after %d checks, want success after 3", err, n)
	}
}

// UntilDone checks until ctx ends, keeping the last check's error and the cause.
func TestUntilDoneContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	n := 0
	err := UntilDone(ctx, "etcd to stop", 10*time.Millisecond, func() error {
		n++
		return errors.New("it listens")
	})
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "waiting for etcd to stop: it listens: context deadline exceeded" || n < 2 {
		t.Errorf("err = %v after %d checks", err, n)
	}
}

// A check whose error carries the context's end already is not followed by it again.
func TestUntilDoneContextEndsInTheCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := UntilDone(ctx, "", time.Hour, func() error { return ctx.Err() })
	if err != context.Canceled {
		t.Errorf("err = %v, want the check's", err)
	}
}

// A permanent error ends UntilDone at once with the error it wraps.
func TestUntilDonePermanent(t *testing.T) {
	refused := errors.New("refused")
	err := UntilDone(context.Background(), "it", time.Hour, func() error { return Permanent{Err: refused} })
	if err != refused {
		t.Errorf("err = %v, want the wrapped error", err)
	}
}
