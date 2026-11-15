package renewal_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/renewal"
	"github.com/trevex/chalkos/pkg/chalkd/renewal/renewaltest"
	"github.com/trevex/chalkos/pkg/internal/testwait"
)

// A check that fails, as before the preparation wrote the control plane's certificates at boot,
// renews nothing: the next check decides again whether anything is due.
func TestRenewalChecksAgainAfterAFailedCheck(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		checks, renewals := 0, 0
		r := renewal.New("test", func(now time.Time) (bool, time.Time, error) {
			checks++
			if checks == 1 {
				return false, time.Time{}, errors.New("no certificates yet")
			}
			return false, now.Add(48 * time.Hour), nil
		}, func(context.Context, time.Time) error {
			renewals++
			return nil
		})
		next := renewaltest.Run(t, r)
		failed := next()
		if !strings.HasPrefix(r.Problem(), "renewal failing: no certificates yet") {
			t.Errorf("problem after a failed check = %q", r.Problem())
		}
		again := next()
		if wait := again.Sub(failed); wait > 2*time.Minute {
			t.Errorf("after a failed check the next one comes after %v", wait)
		}
		if r.Problem() != "" {
			t.Errorf("problem once nothing is due = %q", r.Problem())
		}
		if wait := next().Sub(again); wait < 54*time.Minute {
			t.Errorf("with nothing due the next check comes after %v", wait)
		}
		if renewals != 0 {
			t.Errorf("renewed %d times though nothing was due", renewals)
		}
	})
}

// A renewal that failed is tried again only while it is still due: something else, such as a
// preparation or a delivered certificate, may have renewed in between.
func TestRenewalAfterAFailureRenewsOnlyWhileDue(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		due, renewals := true, 0
		r := renewal.New("test", func(now time.Time) (bool, time.Time, error) {
			if due {
				return true, now.Add(time.Hour), nil
			}
			return false, now.Add(48 * time.Hour), nil
		}, func(context.Context, time.Time) error {
			renewals++
			return errors.New("busy")
		})
		next := renewaltest.Run(t, r)
		next()
		if renewals != 1 || r.Problem() == "" {
			t.Fatalf("renewals %d, problem %q", renewals, r.Problem())
		}
		due = false
		next()
		if renewals != 1 {
			t.Errorf("renewed again though nothing was due any more")
		}
		if r.Problem() != "" {
			t.Errorf("problem once nothing is due = %q", r.Problem())
		}
	})
}

// A forced renewal that failed is tried again until it succeeds, whether due or not.
func TestForcedRenewalIsTriedAgain(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		failing, renewals := true, 0
		r := renewal.New("test", func(now time.Time) (bool, time.Time, error) {
			return false, now.Add(48 * time.Hour), nil
		}, func(context.Context, time.Time) error {
			renewals++
			if failing {
				return errors.New("busy")
			}
			return nil
		})
		next := renewaltest.Run(t, r)
		r.Force()
		forced := next()
		if renewals != 1 {
			t.Fatalf("a forced renewal made %d renewals", renewals)
		}
		failing = false
		retried := next()
		if wait := retried.Sub(forced); wait > 2*time.Minute || renewals != 2 {
			t.Fatalf("after a failed forced renewal: %d renewals, next attempt after %v", renewals, wait)
		}
		if wait := next().Sub(retried); wait < 54*time.Minute || renewals != 2 || r.Problem() != "" {
			t.Errorf("after the forced renewal succeeded: %d renewals, next check after %v, problem %q", renewals, wait, r.Problem())
		}
	})
}

// A renewal without Renew renews nothing: once due it logs that once, checks again about hourly
// and reports no problem, also when forced.
func TestRenewalWithoutRenewOnlyLogs(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	testwait.Bubble(t, func(t *testing.T) {
		due := false
		expires := time.Now().Add(48 * time.Hour)
		r := renewal.New("node certificate", func(time.Time) (bool, time.Time, error) { return due, expires, nil }, nil)
		next := renewaltest.Run(t, r)
		at := next()
		if logged.Len() != 0 {
			t.Fatalf("logged before it is due: %q", logged.String())
		}
		due = true
		for range 3 {
			prev := at
			if at = next(); at.Sub(prev) < 54*time.Minute {
				t.Errorf("the next check comes after %v, want about an hour", at.Sub(prev))
			}
		}
		r.Force()
		forced := next()
		if wait := next().Sub(forced); wait < 54*time.Minute {
			t.Errorf("once forced, the next check comes after %v, want about an hour", wait)
		}
		logs := func() int { return strings.Count(logged.String(), "nothing on this node renews it") }
		if n := logs(); n != 1 {
			t.Errorf("logged %d times that nothing renews it, want once: %q", n, logged.String())
		}
		if !strings.Contains(logged.String(), expires.UTC().Format(time.RFC3339)) {
			t.Errorf("the log does not name the expiry: %q", logged.String())
		}
		if r.Problem() != "" {
			t.Errorf("problem = %q, want none", r.Problem())
		}

		// Once a check finds it no longer due, as after chalkctl node renew, the next time it falls
		// due is logged again.
		due = false
		next()
		due = true
		next()
		if n := logs(); n != 2 {
			t.Errorf("logged %d times that nothing renews it, want twice: %q", n, logged.String())
		}
	})
}
