package nodeip

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func addresses(t *testing.T, list ...string) []Address {
	t.Helper()
	var addrs []Address
	for _, s := range list {
		iface, ip, ok := strings.Cut(s, " ")
		if !ok {
			t.Fatalf("%q is not \"interface address\"", s)
		}
		addrs = append(addrs, Address{Interface: iface, IP: netip.MustParseAddr(ip)})
	}
	return addrs
}

func filter(t *testing.T, subnets ...string) Filter {
	t.Helper()
	f, err := ParseFilter(subnets)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

var reserved = []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16"), netip.MustParsePrefix("10.96.0.0/12")}

func TestParseFilter(t *testing.T) {
	for _, bad := range []string{"10.0.0.0", "10.0.0.0/33", "!", "!fd00::/129", "eth0", ""} {
		if _, err := ParseFilter([]string{bad}); err == nil {
			t.Errorf("ParseFilter(%q) accepted", bad)
		}
	}
	f := filter(t, "10.0.0.0/8", "!10.0.0.10/32", "fd00::/64")
	for ip, want := range map[string]bool{
		"10.1.2.3":  true,
		"10.0.0.10": false,
		"10.0.0.11": true,
		"fd00::5":   true,
		"fd01::5":   false,
		"192.0.2.1": false,
	} {
		if got := f.Matches(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Matches(%s) = %v, want %v", ip, got, want)
		}
	}
	// Only exclusions: every other address matches.
	only := filter(t, "!192.168.0.0/16")
	if !only.Matches(netip.MustParseAddr("10.0.0.1")) || only.Matches(netip.MustParseAddr("192.168.1.1")) {
		t.Error("a filter of exclusions only does not match everything else")
	}
	// A subnet written with host bits matches its whole network.
	if !filter(t, "192.168.100.12/24").Matches(netip.MustParseAddr("192.168.100.99")) {
		t.Error("192.168.100.12/24 does not match 192.168.100.99")
	}
	if got := f.String(); got != "validSubnets 10.0.0.0/8, !10.0.0.10/32, fd00::/64" {
		t.Errorf("String() = %q", got)
	}
	if got := filter(t).String(); got != "the default filter" {
		t.Errorf("String() = %q", got)
	}
}

func TestSelect(t *testing.T) {
	node := addresses(t,
		"lo 127.0.0.1",
		"lo ::1",
		"eth1 fe80::1",
		"eth1 192.168.100.12",
		"eth0 10.0.2.15",
		"eth0 2001:db8::15",
		"flannel.1 10.244.1.0",
		"cni0 10.244.1.1",
		"veth1234 10.88.0.1",
		"kube-ipvs0 10.96.0.1",
		"eth2 169.254.0.5",
	)
	for name, tc := range map[string]struct {
		sel  Selector
		want string
	}{
		// IPv4 first, then by interface name: eth0 before eth1.
		"default":                {Selector{Reserved: reserved}, "10.0.2.15"},
		"validSubnets":           {Selector{Filter: filter(t, "192.168.100.0/24"), Reserved: reserved}, "192.168.100.12"},
		"exclusion":              {Selector{Filter: filter(t, "!10.0.2.0/24"), Reserved: reserved}, "192.168.100.12"},
		"IPv6":                   {Selector{Filter: filter(t, "2001:db8::/32"), Reserved: reserved}, "2001:db8::15"},
		"fixed":                  {Selector{Fixed: netip.MustParseAddr("192.168.100.12"), Filter: filter(t, "10.0.0.0/8"), Reserved: reserved}, "192.168.100.12"},
		"fixed IPv6":             {Selector{Fixed: netip.MustParseAddr("2001:db8::15"), Reserved: reserved}, "2001:db8::15"},
		"pod range":              {Selector{Filter: filter(t, "10.244.0.0/16"), Reserved: reserved}, ""},
		"service range":          {Selector{Filter: filter(t, "10.96.0.0/12"), Reserved: reserved}, ""},
		"CNI interface":          {Selector{Filter: filter(t, "10.88.0.0/16"), Reserved: reserved}, ""},
		"link-local":             {Selector{Filter: filter(t, "169.254.0.0/16", "fe80::/10"), Reserved: reserved}, ""},
		"fixed but not present":  {Selector{Fixed: netip.MustParseAddr("192.168.100.13"), Reserved: reserved}, ""},
		"nothing in the subnets": {Selector{Filter: filter(t, "172.16.0.0/12"), Reserved: reserved}, ""},
	} {
		got, err := tc.sel.Select(node)
		if tc.want == "" {
			var nomatch *NoMatchError
			if !errors.As(err, &nomatch) {
				t.Errorf("%s: Select() = %v, %v, want no match", name, got, err)
			}
			continue
		}
		if err != nil || got != netip.MustParseAddr(tc.want) {
			t.Errorf("%s: Select() = %v, %v, want %s", name, got, err, tc.want)
		}
	}

	// An address on lo that is not a loopback address, as BGP speakers announce them.
	got, err := Selector{Filter: filter(t, "198.51.100.0/24")}.Select(addresses(t, "lo 127.0.0.1", "lo 198.51.100.7"))
	if err != nil || got != netip.MustParseAddr("198.51.100.7") {
		t.Errorf("address on lo: %v, %v", got, err)
	}
}

func TestSelectOrderIsStable(t *testing.T) {
	sel := Selector{Reserved: reserved}
	a := addresses(t, "eth1 192.168.1.5", "eth0 2001:db8::1", "eth1 192.168.1.4", "eth0 10.0.0.9")
	b := []Address{a[3], a[2], a[1], a[0]}
	for _, addrs := range [][]Address{a, b} {
		if got, err := sel.Select(addrs); err != nil || got != netip.MustParseAddr("10.0.0.9") {
			t.Errorf("Select(%v) = %v, %v", addrs, got, err)
		}
	}
	// Within an interface the lower address comes first.
	if got, _ := sel.Select(addresses(t, "eth1 192.168.1.5", "eth1 192.168.1.4")); got != netip.MustParseAddr("192.168.1.4") {
		t.Errorf("Select() = %v", got)
	}
}

func TestSelectEndpointLast(t *testing.T) {
	vip := netip.MustParseAddr("10.0.0.10")
	// The API server's virtual address sorts before the node's own address.
	for _, addrs := range [][]Address{
		addresses(t, "eth0 10.0.0.10", "eth0 10.0.0.11"),
		addresses(t, "eth0 10.0.0.10", "eth1 10.0.0.11"),
		addresses(t, "eth0 10.0.0.10", "eth0 2001:db8::11"),
	} {
		got, err := Selector{Endpoint: vip, Reserved: reserved}.Select(addrs)
		if err != nil || got == vip {
			t.Errorf("Select(%v) = %v, %v, want the node's own address", addrs, got, err)
		}
	}
	// A single control plane whose endpoint is its own address still picks it.
	if got, err := (Selector{Endpoint: vip, Reserved: reserved}).Select(addresses(t, "lo 127.0.0.1", "eth0 10.0.0.10")); err != nil || got != vip {
		t.Errorf("only the endpoint's address: Select() = %v, %v", got, err)
	}
	// A fixed address is picked even when it is the endpoint's.
	if got, err := (Selector{Fixed: vip, Endpoint: vip}).Select(addresses(t, "eth0 10.0.0.10", "eth0 10.0.0.9")); err != nil || got != vip {
		t.Errorf("fixed endpoint address: Select() = %v, %v", got, err)
	}
}

func TestNoMatchError(t *testing.T) {
	sel := Selector{Filter: filter(t, "192.168.100.0/24"), Reserved: reserved}
	_, err := sel.Select(addresses(t, "lo 127.0.0.1", "eth0 fe80::1", "eth0 10.0.2.15", "cni0 10.244.0.1"))
	want := "no node address matches validSubnets 192.168.100.0/24 (the node has 10.244.0.1 on cni0, 10.0.2.15 on eth0)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	_, err = Selector{Fixed: netip.MustParseAddr("10.0.0.11")}.Select(nil)
	if err == nil || err.Error() != "no node address matches nodeIP 10.0.0.11 (the node has no addresses)" {
		t.Errorf("err = %v", err)
	}
}

// fakeClock is a clock whose Sleep only moves its time.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func TestWaitForLateAddress(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	start := clock.now
	calls := 0
	w := Waiter{
		Addresses: func() ([]Address, error) {
			calls++
			switch {
			case calls == 1:
				return nil, errors.New("netlink: interrupted")
			case calls < 5:
				return addresses(t, "eth0 10.0.2.15"), nil
			}
			return addresses(t, "eth0 10.0.2.15", "eth1 192.168.100.12"), nil
		},
		Now:      clock.Now,
		Sleep:    clock.Sleep,
		Interval: time.Second,
	}
	got, err := w.Wait(Selector{Filter: filter(t, "192.168.100.0/24")}, 5*time.Minute)
	if err != nil || got != netip.MustParseAddr("192.168.100.12") {
		t.Fatalf("Wait() = %v, %v", got, err)
	}
	if calls != 5 || clock.now.Sub(start) != 4*time.Second {
		t.Errorf("found after %d looks and %v", calls, clock.now.Sub(start))
	}
}

func TestWaitTimesOut(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	start := clock.now
	calls := 0
	w := Waiter{
		Addresses: func() ([]Address, error) { calls++; return addresses(t, "eth0 10.0.2.15"), nil },
		Now:       clock.Now,
		Sleep:     clock.Sleep,
		Interval:  2 * time.Second,
	}
	_, err := w.Wait(Selector{Filter: filter(t, "192.168.100.0/24")}, 5*time.Second)
	var nomatch *NoMatchError
	if !errors.As(err, &nomatch) {
		t.Fatalf("err = %v, want no match", err)
	}
	// Looks at 0s, 2s, 4s and, once more, at the timeout.
	if calls != 4 || clock.now.Sub(start) != 5*time.Second {
		t.Errorf("gave up after %d looks and %v", calls, clock.now.Sub(start))
	}

	// Without a timeout it looks once.
	calls = 0
	if _, err := w.Wait(Selector{Filter: filter(t, "192.168.100.0/24")}, 0); err == nil || calls != 1 {
		t.Errorf("no timeout: %v after %d looks", err, calls)
	}
}

func TestWaitReportsListingError(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	w := Waiter{
		Addresses: func() ([]Address, error) { return nil, errors.New("netlink: permission denied") },
		Now:       clock.Now,
		Sleep:     clock.Sleep,
		Interval:  time.Second,
	}
	if _, err := w.Wait(Selector{}, 3*time.Second); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v", err)
	}
}
