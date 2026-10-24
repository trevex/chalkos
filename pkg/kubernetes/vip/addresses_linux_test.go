package vip

import (
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
)

// The VIPs go on the interface the cluster names, or else on the one with the node's address of
// their family.
func TestNodeAddresses(t *testing.T) {
	p := knode.Paths{Run: t.TempDir()}
	if err := os.WriteFile(p.NodeIP(), []byte("127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := k8s.Cluster{VIP: k8s.VIP{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.10")}, Mode: "l2"}}
	got, err := NodeAddresses(c, p)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.(*addresses).addrs; len(a) != 1 || a[0].Interface != "lo" || a[0].IP.String() != "10.0.0.10" {
		t.Errorf("VIPs %v, want 10.0.0.10 on lo", a)
	}
	c.VIP.Interface = "bond0"
	got, err = NodeAddresses(c, p)
	if a := got.(*addresses).addrs; err != nil || len(a) != 1 || a[0] != (Address{IP: a[0].IP, Interface: "bond0"}) {
		t.Errorf("VIPs %v, %v, want them on bond0", got, err)
	}
	c.VIP = k8s.VIP{Addresses: []netip.Addr{netip.MustParseAddr("fd00::10")}, Mode: "l2"}
	if _, err := NodeAddresses(c, p); err == nil {
		t.Error("found an interface for an IPv6 VIP on a node without an IPv6 address")
	}
}

// The holder announces only the addresses it added, a few times, and stops once it released
// them.
func TestAnnouncesAddedAddressesOnly(t *testing.T) {
	present := Address{IP: netip.MustParseAddr("10.0.0.10"), Interface: "eth0"}
	missing := Address{IP: netip.MustParseAddr("fd00::10"), Interface: "eth0"}
	var mu sync.Mutex
	announced := map[Address]int{}
	count := func(addr Address) int {
		mu.Lock()
		defer mu.Unlock()
		return announced[addr]
	}
	a := newAddresses([]Address{present, missing})
	a.add = func(addr Address) (bool, error) { return addr != present, nil }
	a.announce = func(addr Address) error {
		mu.Lock()
		defer mu.Unlock()
		announced[addr]++
		return nil
	}
	a.remove = func(Address) error { return nil }
	a.spacing = 50 * time.Millisecond
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the announcements", func() bool { return count(missing) >= 3 })
	if count(present) != 0 || count(missing) != 3 {
		t.Errorf("announced the present address %d times and the added one %d times, want 0 and 3", count(present), count(missing))
	}
	// Both stay on the node: acquiring them again announces nothing.
	a.add = func(Address) (bool, error) { return false, nil }
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if count(missing) != 3 {
		t.Errorf("announced %d times after acquiring present addresses, want 3", count(missing))
	}
	// Released at once after being added again, it is announced no more.
	a.add = func(addr Address) (bool, error) { return addr == missing, nil }
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

// vipControlPlane runs the VIP election of the bootstrapped control plane n1 on its own etcd
