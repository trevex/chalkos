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
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/vip"
)

// vipElectionPrefix is the etcd key prefix of the election whose leader holds the VIPs.
const vipElectionPrefix = "/chalkos/vip"

// localEtcd is where chalkd reaches the etcd member on its own node.
const localEtcd = "https://127.0.0.1:2379"

// AddressManager holds the node's virtual IPs.
type AddressManager interface {
	// Acquire adds the addresses to the node and announces them; again, it adds what went
	// missing and announces them again.
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
	// failures is how many health checks in a row fail before the node resigns.
	failures int
	holder   *atomic.Bool
}

// run takes part in the election until ctx ends, and leaves the VIPs released.
func (v *vipElection) run(ctx context.Context) {
	// A chalkd that stopped without releasing them leaves them behind.
	v.release()
	for ctx.Err() == nil {
		if !v.waitHealthy(ctx) {
			return
		}
		if err := v.lead(ctx); err != nil && ctx.Err() == nil {
			log.Printf("kubernetes: the VIP election: %v", err)
			select {
			case <-ctx.Done():
			case <-time.After(v.interval):
			}
		}
	}
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

// lead campaigns with a new lease and holds the VIPs while the node leads. It stops when the API
// server turned unhealthy, the lease was lost or ctx ended, and releases the VIPs before it
// resigns.
func (v *vipElection) lead(ctx context.Context) error {
	session, err := concurrency.NewSession(v.client, concurrency.WithTTL(v.ttl), concurrency.WithContext(ctx))
	if err != nil {
		return err
	}
	defer session.Close()
	election := concurrency.NewElection(session, vipElectionPrefix)
	campaign, unhealthy := context.WithCancel(ctx)
	defer unhealthy()
	go v.watchHealth(campaign, unhealthy)
	if err := election.Campaign(campaign, v.name); err != nil {
		return err
	}
	resign := func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := election.Resign(rctx); err != nil {
			log.Printf("kubernetes: resign from the VIP election: %v", err)
		}
	}
	if err := v.addrs.Acquire(); err != nil {
		v.release()
		resign()
		return err
	}
	v.holder.Store(true)
	log.Print("kubernetes: this node holds the VIPs")
	for {
		select {
		case <-session.Done():
			v.release()
			return errLeaseLost
		case <-campaign.Done():
			v.release()
			resign()
			return nil
		case <-time.After(v.interval):
			if err := v.addrs.Acquire(); err != nil {
				log.Printf("kubernetes: %v", err)
			}
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
	cred, err := newEtcdCredential(share, credentialValidity, time.Now)
	if err != nil {
		return err
	}
	tlsConfig, err := cred.tlsConfig()
	if err != nil {
		return err
	}
	cli, err := etcd.Dial([]string{localEtcd}, tlsConfig)
	if err != nil {
		return err
	}
	defer cli.Close()
	ttl, interval := k.VIPTTL, k.VIPInterval
	if ttl <= 0 {
		ttl = 10
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	(&vipElection{
		client:   cli,
		name:     n.Name,
		healthy:  func(ctx context.Context) bool { return k.APIServerReady(ctx, share) },
		addrs:    addrs,
		ttl:      ttl,
		interval: interval,
		failures: 3,
		holder:   &k.vipHolder,
	}).run(ctx)
	return ctx.Err()
}

// vipAddresses are the cluster's VIPs on this node: each on the interface the cluster names, or
// else on the one holding the node's address of its family.
type vipAddresses []vip.Address

func (a vipAddresses) Acquire() error {
	for _, addr := range a {
		if err := vip.Add(addr); err != nil {
			return err
		}
		if err := vip.Announce(addr); err != nil {
			return err
		}
	}
	return nil
}

func (a vipAddresses) Release() error {
	var errs []error
	for _, addr := range a {
		errs = append(errs, vip.Remove(addr))
	}
	return errors.Join(errs...)
}

// systemVIPAddresses finds the interfaces of the cluster's VIPs on this node.
func systemVIPAddresses(c k8s.Cluster, p knode.Paths) (AddressManager, error) {
	vips, err := c.VIPAddresses()
	if err != nil {
		return nil, err
	}
	var held vipAddresses
	if c.VIP.Interface != "" {
		for _, ip := range vips {
			held = append(held, vip.Address{IP: ip, Interface: c.VIP.Interface})
		}
		return held, nil
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
	return held, nil
}

// apiServerReady reports whether the node's API server answers ready.
func apiServerReady(ctx context.Context, share kpki.Share) bool {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(share.CA.Certificate)) {
		return false
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kpki.LocalAPIServer+"/readyz", nil)
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
