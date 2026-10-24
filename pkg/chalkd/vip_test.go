package chalkd

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/vip"
	"github.com/trevex/chalkos/pkg/pki"
)

// fakeAddresses stands for a node's VIPs; it counts how often they were released and how often
// they were held at once.
type fakeAddresses struct {
	held     atomic.Bool
	released atomic.Int32
	mu       sync.Mutex
	holders  int
	most     int
}

func (f *fakeAddresses) Acquire() error {
	if !f.held.Swap(true) {
		f.mu.Lock()
		f.holders++
		f.most = max(f.most, f.holders)
		f.mu.Unlock()
	}
	return nil
}

func (f *fakeAddresses) Release() error {
	if f.held.Swap(false) {
		f.mu.Lock()
		f.holders--
		f.mu.Unlock()
	}
	f.released.Add(1)
	return nil
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
// member, with addrs standing for its VIPs and an API server that is always ready.
func vipControlPlane(t *testing.T) (*Kubernetes, *fakeAddresses) {
	t.Helper()
	s, _, _ := memberServer(t)
	k := s.Kubernetes
	data, err := os.ReadFile(k.Paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	write(t, k.Paths.Cluster, strings.Replace(string(data), `"extraArgs"`, `"vip": {"addresses": ["192.168.100.11"], "mode": "l2"}, "extraArgs"`, 1))
	addrs := &fakeAddresses{}
	k.VIPAddresses = func(k8s.Cluster, knode.Paths) (vip.Holder, error) { return addrs, nil }
	k.APIServerReady = func(context.Context, k8s.Cluster, kpki.Share) bool { return true }
	// A lease of seconds keeps a busy machine's delays within its renewals.
	k.VIPTTL, k.VIPInterval = 6, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go k.superviseVIP(ctx, done)
	t.Cleanup(func() { cancel(); <-done })
	return k, addrs
}

// changeShare writes the node's share as change leaves it.
func changeShare(t *testing.T, p knode.Paths, change func(*kpki.Share)) {
	t.Helper()
	share, err := knode.ReadShare(p)
	if err != nil {
		t.Fatal(err)
	}
	change(&share)
	data, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	write(t, p.Share(), string(data))
}

// A share that changes the etcd CAs the node trusts, or the one that issues its credential,
// restarts the VIP election, which dialled etcd with the share it started with: the holder
// releases the VIPs, and takes them again on a connection of the new share. Other changes leave
// the election alone.
func TestVIPRestartsWhenEtcdTrustChanges(t *testing.T) {
	k, addrs := vipControlPlane(t)
	held := func() bool { return addrs.held.Load() && k.vipHolder.Load() }
	eventually(t, "the node holds the VIPs", held)
	released := addrs.released.Load()

	kubernetesCA, err := pki.NewCA("new Kubernetes CA", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	changeShare(t, k.Paths, func(s *kpki.Share) { s.Accepted = &pki.KubernetesAccepted{CA: []string{kubernetesCA.Certificate}} })
	time.Sleep(20 * k.VIPInterval)
	if got := addrs.released.Load(); got != released || !held() {
		t.Fatalf("a share that leaves etcd's trust alone released the VIPs %d times", got-released)
	}

	etcdCA, err := pki.NewCA("new etcd CA", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	changeShare(t, k.Paths, func(s *kpki.Share) { s.Accepted.EtcdCA = []string{etcdCA.Certificate} })
	eventually(t, "the VIPs released and held again", func() bool { return addrs.released.Load() > released && held() })
	addrs.mu.Lock()
	most := addrs.most
	addrs.mu.Unlock()
	if most != 1 {
		t.Errorf("the VIPs were held %d times at once", most)
	}
}
