package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/vip"
)

// vipElectionPrefix is the etcd key prefix of the election whose leader holds the VIPs.
const vipElectionPrefix = "/chalkos/vip"

// electionCleanupTimeout bounds resigning from the election and revoking its lease, which run
// also while chalkd stops.
const electionCleanupTimeout = 3 * time.Second

// errEtcdStalled means etcd did not answer the resignation from a cancelled campaign in time, and
// the election's client was closed.
var errEtcdStalled = errors.New("etcd did not answer the resignation from the VIP election")

// AddressManager holds the node's virtual IPs.
type AddressManager interface {
	// Acquire adds the addresses to the node and announces them; again, it adds what went
	// missing and announces only that.
	Acquire() error
	// Release removes the addresses from the node.
	Release() error
}

// vipElection holds the VIPs on this node while it leads the election, which it campaigns in
// only while its API server is healthy.
type vipElection struct {
	client *clientv3.Client
	// name is the node's name, the leader's value in the election.
	name string
	// healthy reports whether the node's API server is ready.
	healthy func(ctx context.Context) bool
	addrs   AddressManager
	// ttl is the lease's lifetime in seconds: how long the VIPs stay unheld after their holder
	// failed.
	ttl int
	// interval is the time between two health checks, and between two announcements.
	interval time.Duration
	// margin is how long before etcd may let the lease expire the node releases the VIPs, at most
	// a quarter of the lease's lifetime.
	margin time.Duration
	// failures is how many health checks in a row fail before the node resigns.
	failures int
	holder   *atomic.Bool
	// keepAlive renews the lease once; nil means the client's KeepAliveOnce.
	keepAlive func(context.Context, clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error)
	// closed closes the client once, when its campaign stalled or the election ended.
	closed sync.Once
}

// closeClient closes the client; again, it does nothing.
func (v *vipElection) closeClient() {
	v.closed.Do(func() { v.client.Close() })
}

// run takes part in the election until ctx ends or its client was closed, and leaves the VIPs
// released.
func (v *vipElection) run(ctx context.Context) error {
	// A chalkd that stopped without releasing them leaves them behind.
	v.release()
	for ctx.Err() == nil {
		if !v.waitHealthy(ctx) {
			break
		}
		err := v.lead(ctx)
		if ctx.Err() == nil && v.client.Ctx().Err() != nil {
			// The election starts again with a new connection.
			return errEtcdStalled
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("kubernetes: the VIP election: %v", err)
			select {
			case <-ctx.Done():
			case <-time.After(v.interval):
			}
		}
	}
	return ctx.Err()
}

// waitHealthy waits until the API server is ready; false when ctx ended first.
func (v *vipElection) waitHealthy(ctx context.Context) bool {
	for !v.healthy(ctx) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(v.interval):
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
func (v *vipElection) lead(ctx context.Context) error {
	sent := time.Now()
	gctx, cancel := context.WithTimeout(ctx, time.Duration(v.ttl)*time.Second)
	grant, err := v.client.Grant(gctx, int64(v.ttl))
	cancel()
	if err != nil {
		return err
	}
	ttl := time.Duration(grant.TTL) * time.Second
	renewed := &renewals{last: sent, notify: make(chan struct{}, 1)}
	rctx, stopRenewing := context.WithCancel(ctx)
	defer stopRenewing()
	go v.renew(rctx, grant.ID, ttl, renewed)
	// The session outlives ctx, so a stopping node still resigns and revokes its lease, and the
	// next candidate need not wait for the lease to expire.
	sctx, endSession := context.WithCancel(context.Background())
	defer endSession()
	session, err := concurrency.NewSession(v.client, concurrency.WithLease(grant.ID), concurrency.WithTTL(int(grant.TTL)), concurrency.WithContext(sctx))
	if err != nil {
		return err
	}
	defer func() {
		session.Orphan()
		// A closed client would retry the revocation until it timed out; the lease expires.
		if v.client.Ctx().Err() != nil {
			return
		}
		rctx, cancel := context.WithTimeout(context.Background(), electionCleanupTimeout)
		defer cancel()
		if _, err := v.client.Revoke(rctx, grant.ID); err != nil {
			log.Printf("kubernetes: revoke the lease of the VIPs: %v", err)
		}
	}()
	election := concurrency.NewElection(session, vipElectionPrefix)
	campaign, unhealthy := context.WithCancel(ctx)
	defer unhealthy()
	go v.watchHealth(campaign, unhealthy)
	if err := v.campaign(campaign, election); err != nil {
		return err
	}
	resign := func() {
		rctx, cancel := context.WithTimeout(context.Background(), electionCleanupTimeout)
		defer cancel()
		if err := election.Resign(rctx); err != nil {
			log.Printf("kubernetes: resign from the VIP election: %v", err)
		}
	}
	// etcd keeps the lease at least ttl after it received the last renewal, so at least ttl after
	// its request was sent: the node releases the VIPs before then unless a newer renewal
	// succeeded, so no other node takes them while it still holds them.
	margin := min(v.margin, ttl/4)
	if !time.Now().Before(renewed.deadline(ttl, margin)) {
		resign()
		return errLeaseUnrenewed
	}
	fence := time.NewTimer(time.Until(renewed.deadline(ttl, margin)))
	defer fence.Stop()
	if err := v.addrs.Acquire(); err != nil {
		v.release()
		resign()
		return err
	}
	v.holder.Store(true)
	log.Print("kubernetes: this node holds the VIPs")
	acquire := time.NewTicker(v.interval)
	defer acquire.Stop()
	for {
		select {
		case <-session.Done():
			v.release()
			return errLeaseLost
		case <-campaign.Done():
			// The API server turned unhealthy, or chalkd stops.
			v.release()
			resign()
			return nil
		case <-renewed.notify:
			fence.Reset(time.Until(renewed.deadline(ttl, margin)))
		case <-fence.C:
			if d := time.Until(renewed.deadline(ttl, margin)); d > 0 {
				fence.Reset(d)
				continue
			}
			v.release()
			resign()
			return errLeaseUnrenewed
		case <-acquire.C:
			if err := v.addrs.Acquire(); err != nil {
				log.Printf("kubernetes: %v", err)
			}
		}
	}
}

// campaign campaigns until the node leads or ctx ends. Campaign cancelled while it waits resigns
// with the client's own context, which ends only when the client is closed: a connection that
// stays open but no longer answers would block it forever. The client is closed when that
// resignation takes longer than electionCleanupTimeout, and the lease is left to expire.
func (v *vipElection) campaign(ctx context.Context, election *concurrency.Election) error {
	returned := make(chan struct{})
	defer close(returned)
	stop := context.AfterFunc(ctx, func() {
		select {
		case <-returned:
		case <-time.After(electionCleanupTimeout):
			log.Print("kubernetes: etcd does not answer the resignation from the VIP election; closing the connection")
			v.closeClient()
		}
	})
	defer stop()
	return election.Campaign(ctx, v.name)
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
func (v *vipElection) renew(ctx context.Context, id clientv3.LeaseID, ttl time.Duration, r *renewals) {
	next := r.deadline(ttl/3, 0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		sent := time.Now()
		kctx, cancel := context.WithTimeout(ctx, ttl/6)
		resp, err := v.keepAliveOnce(kctx, id)
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
func (v *vipElection) watchHealth(ctx context.Context, unhealthy func()) {
	defer unhealthy()
	failed := 0
	for failed < v.failures {
		select {
		case <-ctx.Done():
			return
		case <-time.After(v.interval):
		}
		if v.healthy(ctx) {
			failed = 0
		} else {
			failed++
		}
	}
	log.Print("kubernetes: the API server is not ready; leaving the VIP election")
}

func (v *vipElection) release() {
	if v.holder.Swap(false) {
		log.Print("kubernetes: this node releases the VIPs")
	}
	if err := v.addrs.Release(); err != nil {
		log.Printf("kubernetes: %v", err)
	}
}

// superviseVIP takes part in the VIP election until ctx ends.
func (k *Kubernetes) superviseVIP(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		err := k.runVIP(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Printf("kubernetes: the VIP election stopped: %v; starting it again", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (k *Kubernetes) runVIP(ctx context.Context) error {
	if prepared, err := knode.Prepared(k.Paths); err != nil {
		return err
	} else if !prepared {
		return errNotPrepared
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return err
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return err
	}
	n, err := k8s.ReadNode(k.Paths.NodeFile)
	if err != nil {
		return err
	}
	addrs, err := k.VIPAddresses(c, k.Paths)
	if err != nil {
		return err
	}
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return err
	}
	ttl, interval := k.VIPTTL, k.VIPInterval
	if ttl <= 0 {
		ttl = 10
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	// The share is read for each check: while the Kubernetes CAs rotate, the API server may serve
	// a certificate of a CA the share this election started with does not hold.
	healthy := func(ctx context.Context) bool {
		current, err := knode.ReadShare(k.Paths)
		if err != nil {
			current = share
		}
		return k.APIServerReady(ctx, c, current)
	}
	election := &vipElection{
		client:   cli,
		name:     n.Name,
		healthy:  healthy,
		addrs:    addrs,
		ttl:      ttl,
		interval: interval,
		margin:   2 * time.Second,
		failures: 3,
		holder:   &k.vipHolder,
	}
	defer election.closeClient()
	return election.run(ctx)
}

// vipAddresses are the cluster's VIPs on this node: each on the interface the cluster names, or
// else on the one holding the node's address of its family.
type vipAddresses struct {
	addrs []vip.Address
	// add, announce and remove change the node's interfaces and tell its neighbours; tests
	// replace them.
	add      func(vip.Address) (bool, error)
	announce func(vip.Address) error
	remove   func(vip.Address) error
	// announcements is how often an added address is announced, spacing apart, so neighbours
	// that missed the first announcement still learn of it.
	announcements int
	spacing       time.Duration

	mu sync.Mutex
	// releases counts the releases, after which earlier announcements stop.
	releases int
}

func newVIPAddresses(addrs []vip.Address) *vipAddresses {
	return &vipAddresses{addrs: addrs, add: vip.Add, announce: vip.Announce, remove: vip.Remove, announcements: 3, spacing: time.Second}
}

// Acquire adds the addresses that are missing and announces those alone: an address that was
// there already was announced when it was added.
func (a *vipAddresses) Acquire() error {
	var errs []error
	var added []vip.Address
	for _, addr := range a.addrs {
		ok, err := a.add(addr)
		if err != nil {
			errs = append(errs, err)
		} else if ok {
			added = append(added, addr)
		}
	}
	if len(added) == 0 {
		return errors.Join(errs...)
	}
	a.mu.Lock()
	releases := a.releases
	errs = append(errs, a.announceLocked(added))
	a.mu.Unlock()
	go func() {
		for range a.announcements - 1 {
			time.Sleep(a.spacing)
			a.mu.Lock()
			if a.releases != releases {
				a.mu.Unlock()
				return
			}
			err := a.announceLocked(added)
			a.mu.Unlock()
			if err != nil {
				log.Printf("kubernetes: %v", err)
			}
		}
	}()
	return errors.Join(errs...)
}

func (a *vipAddresses) announceLocked(addrs []vip.Address) error {
	var errs []error
	for _, addr := range addrs {
		errs = append(errs, a.announce(addr))
	}
	return errors.Join(errs...)
}

// Release stops the announcements and removes the addresses.
func (a *vipAddresses) Release() error {
	a.mu.Lock()
	a.releases++
	a.mu.Unlock()
	var errs []error
	for _, addr := range a.addrs {
		errs = append(errs, a.remove(addr))
	}
	return errors.Join(errs...)
}

// systemVIPAddresses finds the interfaces of the cluster's VIPs on this node.
func systemVIPAddresses(c k8s.Cluster, p knode.Paths) (AddressManager, error) {
	vips, err := c.VIPAddresses()
	if err != nil {
		return nil, err
	}
	var held []vip.Address
	if c.VIP.Interface != "" {
		for _, ip := range vips {
			held = append(held, vip.Address{IP: ip, Interface: c.VIP.Interface})
		}
		return newVIPAddresses(held), nil
	}
	ips, err := knode.ReadNodeIPs(p)
	if err != nil {
		return nil, err
	}
	system, err := nodeip.SystemAddresses()
	if err != nil {
		return nil, err
	}
	for _, ip := range vips {
		i := slices.IndexFunc(system, func(a nodeip.Address) bool {
			if nodeip.FamilyOf(a.IP) != nodeip.FamilyOf(ip) {
				return false
			}
			return slices.ContainsFunc(ips, func(own net.IP) bool {
				addr, ok := netip.AddrFromSlice(own)
				return ok && addr.Unmap() == a.IP
			})
		})
		if i < 0 {
			return nil, fmt.Errorf("the node has no %s address for the VIP %s", nodeip.FamilyOf(ip), ip)
		}
		held = append(held, vip.Address{IP: ip, Interface: system[i].Interface})
	}
	return newVIPAddresses(held), nil
}

// apiServerReady reports whether the node's API server answers ready.
func apiServerReady(ctx context.Context, c k8s.Cluster, share kpki.Share) bool {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(share.CABundle())) {
		return false
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.LocalAPIServer()+"/readyz", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (v *vipElection) keepAliveOnce(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
	if v.keepAlive != nil {
		return v.keepAlive(ctx, id)
	}
	return v.client.KeepAliveOnce(ctx, id)
}
