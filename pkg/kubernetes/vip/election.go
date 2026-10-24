// Package vip holds the cluster's virtual IPs on one control plane: the node that leads an etcd
// election adds them to an interface and announces them to the neighbours, so traffic for them
// reaches it, and removes them when it stops leading.
package vip

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// electionPrefix is the etcd key prefix of the election whose leader holds the VIPs.
const electionPrefix = "/chalkos/vip"

// cleanupTimeout bounds resigning from the election and revoking its lease, which run also while
// the election stops.
const cleanupTimeout = 3 * time.Second

// The defaults of an Election's zero fields.
const (
	DefaultTTL      = 10
	DefaultInterval = 2 * time.Second
	DefaultMargin   = 2 * time.Second
	DefaultFailures = 3
)

// ErrEtcdStalled means etcd did not answer the resignation from a cancelled campaign in time, and
// the election's client was closed.
var ErrEtcdStalled = errors.New("etcd did not answer the resignation from the VIP election")

// Holder holds the node's virtual IPs.
type Holder interface {
	// Acquire adds the addresses to the node and announces them; again, it adds what went
	// missing and announces only that.
	Acquire() error
	// Release removes the addresses from the node.
	Release() error
}

// Election holds the VIPs on this node while it leads the etcd election, which it campaigns in
// only while its API server is healthy.
type Election struct {
	// Client reaches etcd. The election owns it: Close closes it, and so does a campaign that
	// stalled.
	Client *clientv3.Client
	// Name is the node's name, the leader's value in the election.
	Name string
	// Healthy reports whether the node's API server is ready.
	Healthy   func(ctx context.Context) bool
	Addresses Holder
	// TTL is the lease's lifetime in seconds: how long the VIPs stay unheld after their holder
	// failed. Zero means DefaultTTL.
	TTL int
	// Interval is the time between two health checks, and between two announcements. Zero means
	// DefaultInterval.
	Interval time.Duration
	// Margin is how long before etcd may let the lease expire the node releases the VIPs, at most
	// a quarter of the lease's lifetime. Zero means DefaultMargin.
	Margin time.Duration
	// Failures is how many health checks in a row fail before the node resigns. Zero means
	// DefaultFailures.
	Failures int
	// Holding is set while the node holds the VIPs; it may be nil.
	Holding *atomic.Bool
	// keepAlive renews the lease once; nil means the client's KeepAliveOnce.
	keepAlive func(context.Context, clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error)
	// held stands in for a nil Holding.
	held atomic.Bool
	// closed closes the client once, when its campaign stalled or the election ended.
	closed sync.Once
}

// Close closes the client; again, it does nothing.
func (e *Election) Close() {
	e.closed.Do(func() { e.Client.Close() })
}

// Run takes part in the election until ctx ends or its client was closed, and leaves the VIPs
// released.
func (e *Election) Run(ctx context.Context) error {
	// A chalkd that stopped without releasing them leaves them behind.
	e.release()
	for ctx.Err() == nil {
		if !e.waitHealthy(ctx) {
			break
		}
		err := e.lead(ctx)
		if ctx.Err() == nil && e.Client.Ctx().Err() != nil {
			// The election starts again with a new connection.
			return ErrEtcdStalled
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("kubernetes: the VIP election: %v", err)
			select {
			case <-ctx.Done():
			case <-time.After(e.interval()):
			}
		}
	}
	return ctx.Err()
}

// waitHealthy waits until the API server is ready; false when ctx ended first.
func (e *Election) waitHealthy(ctx context.Context) bool {
	for !e.Healthy(ctx) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(e.interval()):
		}
	}
	return true
}

// errLeaseLost means etcd no longer kept the lease that made the node the VIPs' holder.
var errLeaseLost = errors.New("lost the lease of the VIPs")

// errLeaseUnrenewed means the node could not renew the lease in time: etcd may let it expire
// and another node take the VIPs.
var errLeaseUnrenewed = errors.New("could not renew the lease of the VIPs in time")

// lead campaigns with a new lease and holds the VIPs while the node leads. It stops when the API
// server turned unhealthy, the lease was lost or could not be renewed in time, or ctx ended, and
// releases the VIPs before it resigns.
func (e *Election) lead(ctx context.Context) error {
	sent := time.Now()
	gctx, cancel := context.WithTimeout(ctx, time.Duration(e.ttl())*time.Second)
	grant, err := e.Client.Grant(gctx, int64(e.ttl()))
	cancel()
	if err != nil {
		return err
	}
	ttl := time.Duration(grant.TTL) * time.Second
	renewed := &renewals{last: sent, notify: make(chan struct{}, 1)}
	rctx, stopRenewing := context.WithCancel(ctx)
	defer stopRenewing()
	go e.renew(rctx, grant.ID, ttl, renewed)
	// The session outlives ctx, so a stopping node still resigns and revokes its lease, and the
	// next candidate need not wait for the lease to expire.
	sctx, endSession := context.WithCancel(context.Background())
	defer endSession()
	session, err := concurrency.NewSession(e.Client, concurrency.WithLease(grant.ID), concurrency.WithTTL(int(grant.TTL)), concurrency.WithContext(sctx))
	if err != nil {
		return err
	}
	defer func() {
		session.Orphan()
		// A closed client would retry the revocation until it timed out; the lease expires.
		if e.Client.Ctx().Err() != nil {
			return
		}
		rctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := e.Client.Revoke(rctx, grant.ID); err != nil {
			log.Printf("kubernetes: revoke the lease of the VIPs: %v", err)
		}
	}()
	election := concurrency.NewElection(session, electionPrefix)
	campaign, unhealthy := context.WithCancel(ctx)
	defer unhealthy()
	go e.watchHealth(campaign, unhealthy)
	if err := e.campaign(campaign, election); err != nil {
		return err
	}
	resign := func() {
		rctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := election.Resign(rctx); err != nil {
			log.Printf("kubernetes: resign from the VIP election: %v", err)
		}
	}
	// etcd keeps the lease at least ttl after it received the last renewal, so at least ttl after
	// its request was sent: the node releases the VIPs before then unless a newer renewal
	// succeeded, so no other node takes them while it still holds them.
	margin := min(e.margin(), ttl/4)
	if !time.Now().Before(renewed.deadline(ttl, margin)) {
		resign()
		return errLeaseUnrenewed
	}
	fence := time.NewTimer(time.Until(renewed.deadline(ttl, margin)))
	defer fence.Stop()
	if err := e.Addresses.Acquire(); err != nil {
		e.release()
		resign()
		return err
	}
	e.holding().Store(true)
	log.Print("kubernetes: this node holds the VIPs")
	acquire := time.NewTicker(e.interval())
	defer acquire.Stop()
	for {
		select {
		case <-session.Done():
			e.release()
			return errLeaseLost
		case <-campaign.Done():
			// The API server turned unhealthy, or chalkd stops.
			e.release()
			resign()
			return nil
		case <-renewed.notify:
			fence.Reset(time.Until(renewed.deadline(ttl, margin)))
		case <-fence.C:
			if d := time.Until(renewed.deadline(ttl, margin)); d > 0 {
				fence.Reset(d)
				continue
			}
			e.release()
			resign()
			return errLeaseUnrenewed
		case <-acquire.C:
			if err := e.Addresses.Acquire(); err != nil {
				log.Printf("kubernetes: %v", err)
			}
		}
	}
}

// campaign campaigns until the node leads or ctx ends. Campaign cancelled while it waits resigns
// with the client's own context, which ends only when the client is closed: a connection that
// stays open but no longer answers would block it forever. The client is closed when that
// resignation takes longer than cleanupTimeout, and the lease is left to expire.
func (e *Election) campaign(ctx context.Context, election *concurrency.Election) error {
	returned := make(chan struct{})
	defer close(returned)
	stop := context.AfterFunc(ctx, func() {
		select {
		case <-returned:
		case <-time.After(cleanupTimeout):
			log.Print("kubernetes: etcd does not answer the resignation from the VIP election; closing the connection")
			e.Close()
		}
	})
	defer stop()
	return election.Campaign(ctx, e.Name)
}

// renewals holds when the request of the newest successful renewal of the lease was sent.
type renewals struct {
	mu   sync.Mutex
	last time.Time
	// notify receives a value after each successful renewal.
	notify chan struct{}
}

func (r *renewals) renewed(sent time.Time) {
	r.mu.Lock()
	r.last = sent
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// deadline is when the node must have released the VIPs without another renewal.
func (r *renewals) deadline(ttl, margin time.Duration) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last.Add(ttl - margin)
}

// renew renews the lease a third of its lifetime after the last renewal was sent until ctx
// ends. Each attempt gets a sixth of the lifetime; a failed one is retried shortly after, so a
// single stalled renewal does not let the deadline pass.
func (e *Election) renew(ctx context.Context, id clientv3.LeaseID, ttl time.Duration, r *renewals) {
	next := r.deadline(ttl/3, 0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		sent := time.Now()
		kctx, cancel := context.WithTimeout(ctx, ttl/6)
		resp, err := e.keepAliveOnce(kctx, id)
		cancel()
		if err == nil && resp.TTL > 0 {
			r.renewed(sent)
			next = sent.Add(ttl / 3)
		} else {
			next = time.Now().Add(ttl / 20)
		}
	}
}

// watchHealth calls unhealthy once enough health checks in a row failed, or ctx ended.
func (e *Election) watchHealth(ctx context.Context, unhealthy func()) {
	defer unhealthy()
	failed := 0
	for failed < e.failures() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.interval()):
		}
		if e.Healthy(ctx) {
			failed = 0
		} else {
			failed++
		}
	}
	log.Print("kubernetes: the API server is not ready; leaving the VIP election")
}

func (e *Election) release() {
	if e.holding().Swap(false) {
		log.Print("kubernetes: this node releases the VIPs")
	}
	if err := e.Addresses.Release(); err != nil {
		log.Printf("kubernetes: %v", err)
	}
}

func (e *Election) keepAliveOnce(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
	if e.keepAlive != nil {
		return e.keepAlive(ctx, id)
	}
	return e.Client.KeepAliveOnce(ctx, id)
}

func (e *Election) ttl() int {
	if e.TTL <= 0 {
		return DefaultTTL
	}
	return e.TTL
}

func (e *Election) interval() time.Duration {
	if e.Interval <= 0 {
		return DefaultInterval
	}
	return e.Interval
}

func (e *Election) margin() time.Duration {
	if e.Margin <= 0 {
		return DefaultMargin
	}
	return e.Margin
}

func (e *Election) failures() int {
	if e.Failures <= 0 {
		return DefaultFailures
	}
	return e.Failures
}

func (e *Election) holding() *atomic.Bool {
	if e.Holding == nil {
		return &e.held
	}
	return e.Holding
}
