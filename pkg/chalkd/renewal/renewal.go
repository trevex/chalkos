// Package renewal renews a certificate, or a set of them, once it is due, as chalkd renews the
// node certificate and a control plane's certificates.
package renewal

import (
	"context"
	"crypto/rand"
	"log"
	"math/big"
	"sync"
	"time"
)

// Renewal renews a certificate, or a set of them, once it is due, checking about hourly. A failed
// renewal keeps what is there and is tried again with backoff.
type Renewal struct {
	// What names what is renewed, in the log.
	What string
	// Due reports whether the renewal is due at now and when what it renews expires; there being
	// nothing to renew yet is no error.
	Due func(now time.Time) (due bool, expires time.Time, err error)
	// Renew renews.
	Renew func(ctx context.Context, now time.Time) error
	// Now and After are the clock; tests replace them.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
	// Check is the time between two checks; FirstRetry is the first wait after a failure, which
	// doubles up to MaxRetry.
	Check, FirstRetry, MaxRetry time.Duration
	// Timeout bounds each attempt, so a peer that accepts the connection and never answers does
	// not stop the renewal for good.
	Timeout time.Duration

	mu      sync.Mutex
	problem string
	// force asks for a renewal now, whether it is due or not.
	force chan struct{}
}

// New checks about hourly, gives each attempt a minute and retries after a minute, then up to
// hourly.
func New(what string, due func(time.Time) (bool, time.Time, error), renew func(context.Context, time.Time) error) *Renewal {
	return &Renewal{
		What:       what,
		Due:        due,
		Renew:      renew,
		Now:        time.Now,
		After:      time.After,
		Check:      time.Hour,
		FirstRetry: time.Minute,
		MaxRetry:   time.Hour,
		Timeout:    time.Minute,
		force:      make(chan struct{}, 1),
	}
}

// Problem says why the last renewal failed and when what it renews expires; "" while none
// failed.
func (r *Renewal) Problem() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.problem
}

func (r *Renewal) setProblem(problem string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.problem = problem
}

// Run renews when due until ctx ends. The first check comes soon after the start, so a node that
// was off when a renewal fell due renews at once.
func (r *Renewal) Run(ctx context.Context) {
	wait := jitter(r.Check / 10)
	retry := time.Duration(0)
	// forced is a forced renewal that has not succeeded yet. Any other attempt follows a check
	// that found the renewal due: one that failed may have been made unnecessary since, by a
	// preparation or a delivered certificate, and renewing again would restart what runs on it.
	forced := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.After(wait):
		case <-r.force:
			forced = true
		}
		now := r.Now()
		due, expires, err := r.Due(now)
		if err == nil && !forced && !due {
			retry = 0
			r.setProblem("")
			wait = r.Check - r.Check/10 + jitter(r.Check/5)
			continue
		}
		if err == nil {
			attempt, cancel := context.WithTimeout(ctx, r.Timeout)
			err = r.Renew(attempt, now)
			cancel()
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			retry = min(max(2*retry, r.FirstRetry), r.MaxRetry)
			problem := "renewal failing: " + err.Error()
			if !expires.IsZero() {
				problem += "; expires " + expires.UTC().Format(time.RFC3339)
			}
			r.setProblem(problem)
			// Jitter keeps the nodes that lost their control plane together from all asking again
			// at once.
			wait = retry - retry/10 + jitter(retry/5)
			log.Printf("%s: %s; trying again in %v", r.What, problem, wait.Round(time.Second))
			continue
		}
		retry, forced = 0, false
		r.setProblem("")
		log.Printf("%s renewed", r.What)
		wait = r.Check - r.Check/10 + jitter(r.Check/5)
	}
}

// jitter returns a random duration below d, so nodes that started together spread their checks.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

// Force renews at once, whether the renewal is due or not. Only test images use it, after each
// delivery of config.
func (r *Renewal) Force() {
	select {
	case r.force <- struct{}{}:
	default:
		// A forced renewal is pending already.
	}
}
