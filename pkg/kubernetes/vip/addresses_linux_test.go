package vip

import (
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/internal/testwait"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
)

// The VIPs go on the interface the cluster names, or else on the one with the node's address of
// their family.
func TestNodeAddresses(t *testing.T) {
	s := knode.State{Paths: knode.Paths{RunDir: t.TempDir()}}
	if err := os.WriteFile(s.NodeIP(), []byte("127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := k8s.Cluster{VIP: k8s.VIP{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.10")}, Mode: "l2"}}
	got, err := NodeAddresses(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.(*addresses).addrs; len(a) != 1 || a[0].Interface != "lo" || a[0].IP.String() != "10.0.0.10" {
		t.Errorf("VIPs %v, want 10.0.0.10 on lo", a)
	}
	c.VIP.Interface = "bond0"
	got, err = NodeAddresses(c, s)
	if a := got.(*addresses).addrs; err != nil || len(a) != 1 || a[0] != (Address{IP: a[0].IP, Interface: "bond0"}) {
		t.Errorf("VIPs %v, %v, want them on bond0", got, err)
	}
	c.VIP = k8s.VIP{Addresses: []netip.Addr{netip.MustParseAddr("fd00::10")}, Mode: "l2"}
	if _, err := NodeAddresses(c, s); err == nil {
		t.Error("found an interface for an IPv6 VIP on a node without an IPv6 address")
	}
}

// An l2 VIP is announced three times; a routed VIP goes on the interface the node names and is
// announced none, and a routed VIP without an interface is refused.
func TestNodeAddressesModes(t *testing.T) {
	s := knode.State{Paths: knode.Paths{RunDir: t.TempDir()}}
	vips := []netip.Addr{netip.MustParseAddr("10.0.0.10"), netip.MustParseAddr("fd00::10")}
	for _, tc := range []struct {
		mode, iface   string
		announcements int
	}{
		{k8s.VIPModeL2, "node0", 3},
		{k8s.VIPModeRouted, "node0", 0},
	} {
		c := k8s.Cluster{VIP: k8s.VIP{Addresses: vips, Mode: tc.mode, Interface: tc.iface}}
		got, err := NodeAddresses(c, s)
		if err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		a := got.(*addresses)
		if a.announcements != tc.announcements || len(a.addrs) != 2 || a.addrs[0].Interface != tc.iface || a.addrs[1].Interface != tc.iface {
			t.Errorf("%s: VIPs %v announced %d times, want both on %s announced %d times", tc.mode, a.addrs, a.announcements, tc.iface, tc.announcements)
		}
	}
	c := k8s.Cluster{VIP: k8s.VIP{Addresses: vips[:1], Mode: k8s.VIPModeRouted}}
	if _, err := NodeAddresses(c, s); err == nil || !strings.Contains(err.Error(), "vip.interface") {
		t.Errorf("a routed VIP without an interface: %v, want an error naming vip.interface", err)
	}
}

// A routed VIP's holder adds and removes every address and announces none.
func TestRoutedAnnouncesNothing(t *testing.T) {
	addrs := []Address{
		{IP: netip.MustParseAddr("10.0.0.10"), Interface: "node0"},
		{IP: netip.MustParseAddr("fd00::10"), Interface: "node0"},
	}
	var added, removed []Address
	announced := 0
	a := newAddresses(addrs, 0)
	a.add = func(addr Address) (bool, error) { added = append(added, addr); return true, nil }
	a.announce = func(Address) error { announced++; return nil }
	a.remove = func(addr Address) error { removed = append(removed, addr); return nil }
	a.spacing = time.Millisecond
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, addrs) || !slices.Equal(removed, addrs) || announced != 0 {
		t.Errorf("added %v, removed %v, announced %d times; want both added and removed, none announced", added, removed, announced)
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
	a := newAddresses([]Address{present, missing}, 3)
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
	testwait.Eventually(t, "the announcements", func() bool { return count(missing) >= 3 })
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
