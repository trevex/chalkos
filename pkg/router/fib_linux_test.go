package router

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/netnstest"
)

func describeKernel(rs []*KernelRoute) []string {
	var out []string
	for _, r := range rs {
		var hops []string
		for _, h := range r.NextHops {
			hops = append(hops, fmt.Sprintf("%s@%s/%d", h.Gateway, h.Interface, h.Weight))
		}
		out = append(out, fmt.Sprintf("%s %s %s table=%d metric=%d %s", r.Destination, r.Type, r.Protocol, r.Table, r.Metric, strings.Join(hops, ",")))
	}
	return out
}

// mainRoutes lists the kernel's routes of the main table.
func mainRoutes(t *testing.T) []string {
	t.Helper()
	routes, err := KernelRoutes()
	if err != nil {
		t.Fatal(err)
	}
	var main []string
	for _, r := range describeKernel(routes) {
		if strings.Contains(r, " table=254 ") {
			main = append(main, r)
		}
	}
	return main
}

// The kernel's routes as zebra installs them for an unnumbered fabric: the default route and an
// IPv4 route over two IPv6 link-local next hops, of weights of their own; a blackhole; routes on
// the link alone; one of a table above 255; and routes over nexthop objects, whose next hops the
// kernel lists while nexthop_compat_mode is 1, and not when it is 0.
func TestKernelRoutesOfNamespace(t *testing.T) {
	if !netnstest.InNamespace(t) {
		return
	}
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", "eth1", "type", "dummy"},
		{"link", "add", "eth2", "type", "dummy"},
		{"addr", "add", "192.0.2.10/24", "dev", "eth1"},
		{"link", "set", "eth1", "addrgenmode", "none"},
		{"link", "set", "eth2", "addrgenmode", "none"},
		{"link", "set", "eth1", "up"},
		{"link", "set", "eth2", "up"},
		{"-6", "route", "add", "default", "proto", "bgp", "metric", "20",
			"nexthop", "via", "fe80::98fc:f9ff:fe86:8a05", "dev", "eth2", "onlink", "weight", "1",
			"nexthop", "via", "fe80::a43c:43ff:fe37:125d", "dev", "eth1", "onlink", "weight", "2"},
		{"route", "add", "192.0.2.1/32", "proto", "bgp",
			"nexthop", "via", "inet6", "fe80::98fc:f9ff:fe86:8a05", "dev", "eth2", "onlink",
			"nexthop", "via", "inet6", "fe80::a43c:43ff:fe37:125d", "dev", "eth1", "onlink"},
		{"-6", "route", "add", "blackhole", "2001:db8:2::/48", "proto", "boot"},
		{"-6", "route", "add", "2001:db8:3::/64", "dev", "eth1", "proto", "kernel", "metric", "256"},
		{"route", "add", "198.51.100.0/24", "via", "192.0.2.254", "dev", "eth1", "table", "1000", "proto", "static"},
		{"nexthop", "add", "id", "1", "via", "fe80::1", "dev", "eth1", "onlink"},
		{"nexthop", "add", "id", "2", "via", "fe80::2", "dev", "eth2", "onlink"},
		{"nexthop", "add", "id", "3", "group", "1,1/2,3"},
		{"-6", "route", "add", "2001:db8:9::/48", "nhid", "3", "proto", "bgp", "metric", "20"},
		{"route", "add", "203.0.113.0/24", "nhid", "1", "proto", "bgp", "metric", "20"},
	} {
		netnstest.IP(t, args...)
	}

	want := []string{
		"192.0.2.0/24 unicast kernel table=254 metric=0 @eth1/0",
		"192.0.2.1/32 unicast bgp table=254 metric=0 fe80::98fc:f9ff:fe86:8a05@eth2/1,fe80::a43c:43ff:fe37:125d@eth1/1",
		"203.0.113.0/24 unicast bgp table=254 metric=20 fe80::1@eth1/0",
		"::/0 unicast bgp table=254 metric=20 fe80::98fc:f9ff:fe86:8a05@eth2/1,fe80::a43c:43ff:fe37:125d@eth1/2",
		"2001:db8:2::/48 blackhole boot table=254 metric=1024 @lo/0",
		"2001:db8:3::/64 unicast kernel table=254 metric=256 @eth1/0",
		"2001:db8:9::/48 unicast bgp table=254 metric=20 fe80::1@eth1/1,fe80::2@eth2/3",
	}
	got := mainRoutes(t)
	if !slices.Equal(got, want) {
		t.Errorf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	routes, err := KernelRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if want := "198.51.100.0/24 unicast static table=1000 metric=0 192.0.2.254@eth1/0"; !slices.Contains(describeKernel(routes), want) {
		t.Errorf("no route %q in %v", want, describeKernel(routes))
	}

	if err := os.WriteFile("/proc/sys/net/ipv4/nexthop_compat_mode", []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range mainRoutes(t) {
		if strings.HasPrefix(r, "2001:db8:9::/48 ") && r != "2001:db8:9::/48 unicast bgp table=254 metric=20 " {
			t.Errorf("route over a nexthop object without nexthop_compat_mode: %s", r)
		}
	}
}

// The node's own routes come from netlink: every network namespace has its loopback's.
func TestKernelRoutes(t *testing.T) {
	routes, err := KernelRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(routes, func(r *KernelRoute) bool {
		return r.Destination == "127.0.0.1/32" && r.Type == "local" && r.Table == 255
	}) {
		t.Errorf("no local route of 127.0.0.1 in %v", describeKernel(routes))
	}
}
