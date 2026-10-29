package renewal

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/renewal/renewaltest"
)

// runRenewal runs r on a fake clock until the test ends and returns the clock and a function
// that ends the current wait at the time given and returns the next wait.
func runRenewal(t *testing.T, r *Renewal, start time.Time) (*renewaltest.Clock, func(time.Time) time.Duration) {
	t.Helper()
	clock := renewaltest.NewClock(start)
	r.Now = clock.Now
	r.After = clock.After
	return clock, renewaltest.Run(t, r.Run, clock)
}

// A check that fails, as before the preparation wrote the control plane's certificates at boot,
// renews nothing: the next check decides again whether anything is due.
func TestRenewalChecksAgainAfterAFailedCheck(t *testing.T) {
	start := time.Now()
	checks, renewals := 0, 0
	r := New("test", func(time.Time) (bool, time.Time, error) {
		checks++
		if checks == 1 {
			return false, time.Time{}, errors.New("no certificates yet")
		}
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error {
		renewals++
		return nil
	})
	_, advance := runRenewal(t, r, start)
	if wait := advance(start); wait > 2*time.Minute {
		t.Errorf("after a failed check the next one comes after %v", wait)
	}
	if !strings.HasPrefix(r.Problem(), "renewal failing: no certificates yet") {
		t.Errorf("problem after a failed check = %q", r.Problem())
	}
	if wait := advance(start.Add(time.Minute)); wait < 54*time.Minute {
		t.Errorf("with nothing due the next check comes after %v", wait)
	}
	if renewals != 0 {
		t.Errorf("renewed %d times though nothing was due", renewals)
	}
	if r.Problem() != "" {
		t.Errorf("problem once nothing is due = %q", r.Problem())
	}
}

// A renewal that failed is tried again only while it is still due: something else, such as a
// preparation or a delivered certificate, may have renewed in between.
func TestRenewalAfterAFailureRenewsOnlyWhileDue(t *testing.T) {
	start := time.Now()
	due, renewals := true, 0
	r := New("test", func(time.Time) (bool, time.Time, error) {
		if due {
			return true, start.Add(time.Hour), nil
		}
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error {
		renewals++
		return errors.New("busy")
	})
	_, advance := runRenewal(t, r, start)
	advance(start)
	if renewals != 1 || r.Problem() == "" {
		t.Fatalf("renewals %d, problem %q", renewals, r.Problem())
	}
	due = false
	advance(start.Add(time.Minute))
	if renewals != 1 {
		t.Errorf("renewed again though nothing was due any more")
	}
	if r.Problem() != "" {
		t.Errorf("problem once nothing is due = %q", r.Problem())
	}
}

// A renewal started again keeps no problem from an earlier run once a check finds nothing due.
func TestRenewalClearsAnEarlierProblem(t *testing.T) {
	start := time.Now()
	r := New("test", func(time.Time) (bool, time.Time, error) {
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error { return nil })
	r.setProblem("renewal failing: connection refused; expires 2027-10-08T12:00:00Z")
	_, advance := runRenewal(t, r, start)
	advance(start)
	if r.Problem() != "" {
		t.Errorf("problem once nothing is due = %q", r.Problem())
	}
}

// A forced renewal that failed is tried again until it succeeds, whether due or not.
func TestForcedRenewalIsTriedAgain(t *testing.T) {
	start := time.Now()
	failing, renewals := true, 0
	r := New("test", func(time.Time) (bool, time.Time, error) {
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error {
		renewals++
		if failing {
			return errors.New("busy")
		}
		return nil
	})
	clock, advance := runRenewal(t, r, start)
	r.Force()
	if wait := clock.Next(t); wait > 2*time.Minute || renewals != 1 {
		t.Fatalf("after a failed forced renewal: %d renewals, next attempt after %v", renewals, wait)
	}
	failing = false
	if wait := advance(start.Add(time.Minute)); wait < 54*time.Minute || renewals != 2 {
		t.Errorf("after a forced renewal: %d renewals, next check after %v", renewals, wait)
	}
	advance(start.Add(time.Hour))
	if renewals != 2 || r.Problem() != "" {
		t.Errorf("after the forced renewal succeeded: %d renewals, problem %q", renewals, r.Problem())
	}
}

// A renewal without Renew renews nothing: once due it logs that once, checks again about hourly
// and reports no problem, also when forced.
func TestRenewalWithoutRenewOnlyLogs(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	start := time.Now()
	due := false
	expires := start.Add(48 * time.Hour)
	r := New("node certificate", func(time.Time) (bool, time.Time, error) { return due, expires, nil }, nil)
	clock, advance := runRenewal(t, r, start)
	if wait := advance(start); wait < 54*time.Minute || logged.Len() != 0 {
		t.Fatalf("before it is due: next check after %v, logged %q", wait, logged.String())
	}
	due = true
	for i := range 3 {
		if wait := advance(start.Add(time.Duration(i+1) * time.Hour)); wait < 54*time.Minute {
			t.Errorf("once due, the next check comes after %v, want about an hour", wait)
		}
	}
	r.Force()
	if wait := clock.Next(t); wait < 54*time.Minute {
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
	advance(start.Add(5 * time.Hour))
	due = true
	advance(start.Add(6 * time.Hour))
	if n := logs(); n != 2 {
		t.Errorf("logged %d times that nothing renews it, want twice: %q", n, logged.String())
	}
}
