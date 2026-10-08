package chalkd

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	"github.com/trevex/chalkos/pkg/kubernetes/vip"
	"github.com/trevex/chalkos/pkg/pki"
)

// fakeAddresses stands for a node's VIPs; holders counts the nodes that hold them.
type fakeAddresses struct {
	held     atomic.Bool
	released atomic.Int32
	holders  *holders
	// releasedAt is when the node last released addresses it held.
	mu         sync.Mutex
	releasedAt time.Time
}

type holders struct {
	mu       sync.Mutex
	now, max int
}

func (h *holders) change(d int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now += d
	h.max = max(h.max, h.now)
}

func (h *holders) count() (now, most int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now, h.max
}

func (f *fakeAddresses) Acquire() error {
	if !f.held.Swap(true) {
		f.holders.change(1)
	}
	return nil
}

func (f *fakeAddresses) Release() error {
	if f.held.Swap(false) {
		f.mu.Lock()
		f.releasedAt = time.Now()
		f.mu.Unlock()
		f.holders.change(-1)
	}
	f.released.Add(1)
	return nil
}

// electionNode is a control plane taking part in the election, healthy while healthy is set.
type electionNode struct {
	election *vipElection
	addrs    *fakeAddresses
	healthy  atomic.Bool
	// renewals counts the renewals of its leases that succeeded.
	renewals atomic.Int32
	stop     context.CancelFunc
	done     chan struct{}
	// err is why its election ended, once done is closed.
	err error
}

// electionTTL is the lifetime in seconds of the leases in the election tests. Each renewal gets a
// sixth of it, and the holder releases the VIPs a quarter of it before etcd may let the lease
// expire: a lifetime of seconds keeps a busy machine's delays well within both.
const electionTTL = 6

// startElection runs the election of n nodes on the etcd cluster of m.
func startElection(t *testing.T, ca pki.CertKey, m *etcdtest.Member, n int) ([]*electionNode, *holders) {
	t.Helper()
	h := &holders{}
	var nodes []*electionNode
	for i := range n {
		nodes = append(nodes, startNode(t, ca, m.ClientURL, "cp"+string(rune('1'+i)), electionTTL, h))
	}
	return nodes, h
}

// startNode runs the election on a node of the name that reaches etcd at url, with a lease of
// ttl seconds.
func startNode(t *testing.T, ca pki.CertKey, url, name string, ttl int, h *holders) *electionNode {
	t.Helper()
	return startNodeWith(t, ca, url, name, ttl, h, nil)
}

// startNodeWith is startNode with the election changed by configure before it runs.
func startNodeWith(t *testing.T, ca pki.CertKey, url, name string, ttl int, h *holders, configure func(*vipElection)) *electionNode {
	t.Helper()
	cli, err := etcd.Dial([]string{url}, etcdtest.ClientTLS(t, ca))
	if err != nil {
		t.Fatal(err)
	}
	node := &electionNode{addrs: &fakeAddresses{holders: h}, done: make(chan struct{})}
	node.healthy.Store(true)
	node.election = &vipElection{
		client:   cli,
		name:     name,
		healthy:  func(context.Context) bool { return node.healthy.Load() },
		addrs:    node.addrs,
		ttl:      ttl,
		interval: 50 * time.Millisecond,
		margin:   2 * time.Second,
		failures: 3,
		holder:   &atomic.Bool{},
	}
	t.Cleanup(node.election.closeClient)
	if configure != nil {
		configure(node.election)
	}
	keepAlive := node.election.keepAlive
	if keepAlive == nil {
		keepAlive = cli.KeepAliveOnce
	}
	node.election.keepAlive = func(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
		resp, err := keepAlive(ctx, id)
		if err == nil && resp.TTL > 0 {
			node.renewals.Add(1)
		}
		return resp, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	node.stop = cancel
	go func() {
		defer close(node.done)
		node.err = node.election.run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-node.done })
	return node
}

// holder waits until exactly one node holds the VIPs and returns it.
func holder(t *testing.T, nodes []*electionNode) *electionNode {
	t.Helper()
	var found *electionNode
	eventually(t, "one holder of the VIPs", func() bool {
		found = nil
		count := 0
		for _, n := range nodes {
			if n.addrs.held.Load() {
				found = n
				count++
			}
		}
		return count == 1 && found.election.holder.Load()
	})
	return found
}

func TestVIPOneHolder(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	nodes, h := startElection(t, ca, m, 3)
	first := holder(t, nodes)
	// Three renewals span the lease's lifetime.
	renewed := first.renewals.Load()
	eventually(t, "renewals of the holder's lease", func() bool { return first.renewals.Load() >= renewed+3 })
	if now, most := h.count(); now != 1 || most != 1 {
		t.Errorf("%d nodes hold the VIPs, at most %d; want one", now, most)
	}
	if holder(t, nodes) != first {
		t.Error("the VIPs moved while their holder was healthy")
	}
	// Every node released what an earlier chalkd might have left behind before it campaigned.
	for _, n := range nodes {
		if n.addrs.released.Load() == 0 {
			t.Error("a node campaigned without releasing leftovers first")
		}
	}
}

// A holder whose API server turns unhealthy releases the VIPs and resigns; another healthy node
// takes them.
func TestVIPMovesWhenUnhealthy(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	nodes, h := startElection(t, ca, m, 2)
	first := holder(t, nodes)
	first.healthy.Store(false)
	eventually(t, "the unhealthy node to release the VIPs", func() bool { return !first.addrs.held.Load() })
	if second := holder(t, nodes); second == first {
		t.Fatal("the unhealthy node holds the VIPs again")
	}
	second := holder(t, nodes)
	// Healthy again, it waits as a standby.
	first.healthy.Store(true)
	time.Sleep(500 * time.Millisecond)
	if holder(t, nodes) != second {
		t.Error("the VIPs moved back")
	}
	if _, most := h.count(); most != 1 {
		t.Errorf("at most %d nodes held the VIPs at once", most)
	}
}

// A holder whose lease etcd revoked, as when it lost touch with etcd, releases the VIPs.
func TestVIPReleasedOnLeaseLoss(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	nodes, _ := startElection(t, ca, m, 1)
	first := holder(t, nodes)
	released := first.addrs.released.Load()
	cli := first.election.client
	resp, err := cli.Get(context.Background(), vipElectionPrefix, clientv3.WithFirstCreate()...)
	if err != nil || len(resp.Kvs) != 1 {
		t.Fatalf("the election's leader: %v, %v", resp, err)
	}
	if _, err := cli.Revoke(context.Background(), clientv3.LeaseID(resp.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the release", func() bool { return first.addrs.released.Load() > released })
	// Healthy, it campaigns with a new lease and holds them again.
	holder(t, nodes)
}

// A stopping chalkd releases the VIPs.
func TestVIPReleasedOnStop(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	nodes, h := startElection(t, ca, m, 1)
	first := holder(t, nodes)
	first.stop()
	<-first.done
	if now, _ := h.count(); now != 0 || first.election.holder.Load() {
		t.Error("a stopped node holds the VIPs")
	}
}

// A stopping holder resigns, so the next candidate takes the VIPs without waiting for its lease
// to expire.
func TestVIPResignedOnStop(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	h := &holders{}
	current := startNode(t, ca, m.ClientURL, "cp1", 60, h)
	holder(t, []*electionNode{current})
	for i := range 4 {
		next := startNode(t, ca, m.ClientURL, "cp"+string(rune('2'+i)), 60, h)
		time.Sleep(200 * time.Millisecond)
		stopped := time.Now()
		current.stop()
		<-current.done
		eventually(t, "the next candidate to hold the VIPs", func() bool { return next.addrs.held.Load() })
		if d := time.Since(stopped); d > 10*time.Second {
			t.Fatalf("the next candidate took the VIPs %v after the holder stopped", d)
		}
		current = next
	}
	if _, most := h.count(); most != 1 {
		t.Errorf("%d nodes held the VIPs at once", most)
	}
}

// A holder cut off from etcd releases the VIPs before etcd lets its lease expire, and so before
// another node takes them, also when etcd's replies reached it late.
func TestVIPReleasedBeforeLeaseExpires(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	h := &holders{}
	proxy := etcdtest.NewProxy(t, m.ClientURL, 400*time.Millisecond)
	first := startNode(t, ca, proxy.URL, "cp1", electionTTL, h)
	holder(t, []*electionNode{first})
	second := startNode(t, ca, m.ClientURL, "cp2", electionTTL, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cli := second.election.client
	resp, err := cli.Get(ctx, vipElectionPrefix, clientv3.WithFirstCreate()...)
	if err != nil || len(resp.Kvs) != 1 {
		t.Fatalf("the election's leader: %v, %v", resp, err)
	}
	lease := clientv3.LeaseID(resp.Kvs[0].Lease)
	// Renewals go back and forth a few times.
	renewed := first.renewals.Load()
	eventually(t, "renewals of the holder's lease", func() bool { return first.renewals.Load() >= renewed+2 })
	revoked := make(chan time.Time, 1)
	go func() {
		for ctx.Err() == nil {
			if ttl, err := cli.TimeToLive(ctx, lease); err == nil && ttl.TTL == -1 {
				revoked <- time.Now()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	proxy.Pause()
	var revokedAt time.Time
	select {
	case revokedAt = <-revoked:
	case <-time.After(time.Minute):
		t.Fatal("etcd did not revoke the lease of the cut-off holder")
	}
	eventually(t, "the second node to hold the VIPs", func() bool { return second.addrs.held.Load() })
	if first.addrs.held.Load() {
		t.Fatal("the cut-off holder holds the VIPs")
	}
	first.addrs.mu.Lock()
	releasedAt := first.addrs.releasedAt
	first.addrs.mu.Unlock()
	if !releasedAt.Before(revokedAt) {
		t.Errorf("the cut-off holder released the VIPs %v after etcd revoked its lease", releasedAt.Sub(revokedAt))
	}
	if _, most := h.count(); most != 1 {
		t.Errorf("%d nodes held the VIPs at once", most)
	}
}

// stalledStandby runs the election of a holder and a standby whose campaign waits for the holder's
// key to go, then lets the standby's connection to etcd stay open but no longer answer.
func stalledStandby(t *testing.T) (first, standby *electionNode) {
	t.Helper()
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	h := &holders{}
	first = startNode(t, ca, m.ClientURL, "cp1", electionTTL, h)
	holder(t, []*electionNode{first})
	proxy := etcdtest.NewProxy(t, m.ClientURL, 0)
	standby = startNode(t, ca, proxy.URL, "cp2", electionTTL, h)
	cli := first.election.client
	eventually(t, "the standby's campaign", func() bool {
		resp, err := cli.Get(context.Background(), vipElectionPrefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
		return err == nil && resp.Count == 2
	})
	// Renewals sent after its key was written return after the reply to its campaign: it waits
	// for the holder's key to go.
	renewed := standby.renewals.Load()
	eventually(t, "renewals of the standby's lease", func() bool { return standby.renewals.Load() >= renewed+2 })
	proxy.Pause()
	return first, standby
}

// waitStalled waits for the election of the standby to end, which it must once its cancelled
// campaign gave up resigning.
func waitStalled(t *testing.T, first, standby *electionNode, want error) {
	t.Helper()
	select {
	case <-standby.done:
	case <-time.After(electionCleanupTimeout + 3*time.Second):
		// Unblock the election, so the test's cleanup does not wait for it forever.
		standby.election.closeClient()
		<-standby.done
		t.Fatal("the standby's election did not end while etcd did not answer")
	}
	if standby.err != want {
		t.Errorf("the standby's election ended with %v, want %v", standby.err, want)
	}
	if standby.addrs.held.Load() || !first.addrs.held.Load() {
		t.Error("the VIPs moved to the standby")
	}
}

// A stopping standby whose connection to etcd stays open but no longer answers stops in time:
// the resignation the cancelled campaign starts on its own would wait for an answer forever.
func TestVIPStandbyStopsWhenEtcdStalls(t *testing.T) {
	first, standby := stalledStandby(t)
	standby.stop()
	waitStalled(t, first, standby, context.Canceled)
}

// A standby whose API server turned unhealthy while its connection to etcd stalled leaves the
// election, which starts again with a new connection.
func TestVIPUnhealthyStandbyLeavesWhenEtcdStalls(t *testing.T) {
	first, standby := stalledStandby(t)
	standby.healthy.Store(false)
	waitStalled(t, first, standby, errEtcdStalled)
}

// A healthy holder whose lease is renewed keeps the VIPs, also over a slow network.
func TestVIPKeptWhileRenewed(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	h := &holders{}
	proxy := etcdtest.NewProxy(t, m.ClientURL, 200*time.Millisecond)
	first := startNode(t, ca, proxy.URL, "cp1", electionTTL, h)
	holder(t, []*electionNode{first})
	released := first.addrs.released.Load()
	// Three renewals span the lease's lifetime.
	renewed := first.renewals.Load()
	eventually(t, "renewals of the holder's lease", func() bool { return first.renewals.Load() >= renewed+3 })
	if first.addrs.released.Load() != released || !first.addrs.held.Load() {
		t.Error("the holder dropped the VIPs while its lease was renewed")
	}
}

// A healthy holder keeps the VIPs when one renewal of its lease stalls: the next one is tried
// once the stalled one timed out, in time before the holder would have to release them.
func TestVIPKeptWhenOneRenewalStalls(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	h := &holders{}
	var calls atomic.Int32
	first := startNodeWith(t, ca, m.ClientURL, "cp1", electionTTL, h, func(v *vipElection) {
		v.keepAlive = func(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return v.client.KeepAliveOnce(ctx, id)
		}
	})
	holder(t, []*electionNode{first})
	released := first.addrs.released.Load()
	// The third renewal starts after the holder would have released the VIPs without the second.
	eventually(t, "the stalled renewal and later ones", func() bool { return calls.Load() >= 3 })
	if first.addrs.released.Load() != released || !first.addrs.held.Load() {
		t.Error("the holder dropped the VIPs after one stalled renewal")
	}
}

// A holder whose renewals keep failing releases the VIPs before etcd could let the lease expire
// after the last renewal that succeeded.
func TestVIPReleasedWhenRenewalsFail(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	h := &holders{}
	var failing atomic.Bool
	var failed atomic.Int32
	var mu sync.Mutex
	var lastSent time.Time
	first := startNodeWith(t, ca, m.ClientURL, "cp1", electionTTL, h, func(v *vipElection) {
		v.keepAlive = func(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
			if failing.Load() {
				failed.Add(1)
				return nil, context.DeadlineExceeded
			}
			sent := time.Now()
			resp, err := v.client.KeepAliveOnce(ctx, id)
			if err == nil {
				mu.Lock()
				lastSent = sent
				mu.Unlock()
			}
			return resp, err
		}
	})
	holder(t, []*electionNode{first})
	eventually(t, "a renewal", func() bool { return first.renewals.Load() > 0 })
	failing.Store(true)
	eventually(t, "the release", func() bool { return !first.addrs.held.Load() })
	first.addrs.mu.Lock()
	releasedAt := first.addrs.releasedAt
	first.addrs.mu.Unlock()
	mu.Lock()
	last := lastSent
	mu.Unlock()
	expiry := last.Add(electionTTL * time.Second)
	if last.IsZero() {
		t.Fatal("no renewal succeeded before they failed")
	}
	if !releasedAt.Before(expiry) {
		t.Errorf("the holder released the VIPs %v after etcd could let its lease expire", releasedAt.Sub(expiry))
	}
	if failed.Load() < 2 {
		t.Errorf("%d failed renewals before the release, want retries", failed.Load())
	}
}

func TestStatusVIP(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	write(t, k.Paths.Bootstrapped(), "")
	data, err := os.ReadFile(k.Paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	cluster := strings.Replace(string(data), `"extraArgs"`, `"vip": {"addresses": ["192.168.100.11"], "mode": "l2"}, "extraArgs"`, 1)
	write(t, k.Paths.Cluster, cluster)
	if st, err := k.status(context.Background()); err != nil || st.Vip != "standby" {
		t.Errorf("status %v, %v, want a standby", st, err)
	}
	k.vipHolder.Store(true)
	if st, err := k.status(context.Background()); err != nil || st.Vip != "holder" {
		t.Errorf("status %v, %v, want the holder", st, err)
	}
}

// The VIPs go on the interface the cluster names, or else on the one with the node's address of
// their family.
func TestSystemVIPAddresses(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	p := s.Kubernetes.Paths
	write(t, p.NodeIP(), "127.0.0.1\n")
	c := k8s.Cluster{VIP: k8s.VIP{Addresses: []string{"10.0.0.10"}, Mode: "l2"}}
	got, err := systemVIPAddresses(c, p)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.(*vipAddresses).addrs; len(a) != 1 || a[0].Interface != "lo" || a[0].IP.String() != "10.0.0.10" {
		t.Errorf("VIPs %v, want 10.0.0.10 on lo", a)
	}
	c.VIP.Interface = "bond0"
	got, err = systemVIPAddresses(c, p)
	if a := got.(*vipAddresses).addrs; err != nil || len(a) != 1 || a[0] != (vip.Address{IP: a[0].IP, Interface: "bond0"}) {
		t.Errorf("VIPs %v, %v, want them on bond0", got, err)
	}
	c.VIP = k8s.VIP{Addresses: []string{"fd00::10"}, Mode: "l2"}
	if _, err := systemVIPAddresses(c, p); err == nil {
		t.Error("found an interface for an IPv6 VIP on a node without an IPv6 address")
	}
}

// The holder announces only the addresses it added, a few times, and stops once it released
// them.
func TestVIPAnnouncesAddedAddressesOnly(t *testing.T) {
	present := vip.Address{IP: netip.MustParseAddr("10.0.0.10"), Interface: "eth0"}
	missing := vip.Address{IP: netip.MustParseAddr("fd00::10"), Interface: "eth0"}
	var mu sync.Mutex
	announced := map[vip.Address]int{}
	count := func(addr vip.Address) int {
		mu.Lock()
		defer mu.Unlock()
		return announced[addr]
	}
	a := newVIPAddresses([]vip.Address{present, missing})
	a.add = func(addr vip.Address) (bool, error) { return addr != present, nil }
	a.announce = func(addr vip.Address) error {
		mu.Lock()
		defer mu.Unlock()
		announced[addr]++
		return nil
	}
	a.remove = func(vip.Address) error { return nil }
	a.spacing = 50 * time.Millisecond
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the announcements", func() bool { return count(missing) >= 3 })
	if count(present) != 0 || count(missing) != 3 {
		t.Errorf("announced the present address %d times and the added one %d times, want 0 and 3", count(present), count(missing))
	}
	// Both stay on the node: acquiring them again announces nothing.
	a.add = func(vip.Address) (bool, error) { return false, nil }
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if count(missing) != 3 {
		t.Errorf("announced %d times after acquiring present addresses, want 3", count(missing))
	}
	// Released at once after being added again, it is announced no more.
	a.add = func(addr vip.Address) (bool, error) { return addr == missing, nil }
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if count(missing) != 4 || count(present) != 0 {
		t.Errorf("announced %d and %d times after the release, want 4 and 0", count(missing), count(present))
	}
}
