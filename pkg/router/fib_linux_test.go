package router

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

type rtattr struct {
	typ   uint16
	value []byte
}

// attrs encodes netlink attributes, each padded to 4 bytes.
func attrs(as ...rtattr) []byte {
	var data []byte
	for _, a := range as {
		n := syscall.SizeofRtAttr + len(a.value)
		b := make([]byte, (n+3)&^3)
		binary.NativeEndian.PutUint16(b[0:], uint16(n))
		binary.NativeEndian.PutUint16(b[2:], a.typ)
		copy(b[syscall.SizeofRtAttr:], a.value)
		data = append(data, b...)
	}
	return data
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, v)
	return b
}

func ip(s string) []byte { return netip.MustParseAddr(s).AsSlice() }

// viaIPv6 is an RTA_VIA of an IPv6 address.
func viaIPv6(s string) []byte {
	b := make([]byte, 2)
	binary.NativeEndian.PutUint16(b, syscall.AF_INET6)
	return append(b, ip(s)...)
}

// nexthop encodes a struct rtnexthop of weight hops+1 on the interface, with attributes.
func nexthop(hops uint8, index uint32, as ...rtattr) []byte {
	body := attrs(as...)
	b := make([]byte, 8)
	binary.NativeEndian.PutUint16(b[0:], uint16(8+len(body)))
	b[3] = hops
	binary.NativeEndian.PutUint32(b[4:], index)
	return append(b, body...)
}

// routeMessage builds an RTM_NEWROUTE message as the kernel sends it.
func routeMessage(family, bits, table, protocol, typ uint8, as ...rtattr) syscall.NetlinkMessage {
	data := make([]byte, syscall.SizeofRtMsg)
	data[0], data[1], data[4], data[5], data[7] = family, bits, table, protocol, typ
	data = append(data, attrs(as...)...)
	return syscall.NetlinkMessage{
		Header: syscall.NlMsghdr{Len: uint32(syscall.NLMSG_HDRLEN + len(data)), Type: syscall.RTM_NEWROUTE},
		Data:   data,
	}
}

func describeKernel(rs []*nodev1.KernelRoute) []string {
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

// The kernel's routes as zebra installs them for an unnumbered fabric: the default route and an
// IPv4 route over two IPv6 link-local next hops, a blackhole, a route on the link alone, one of a
// table above 255, and the end of the dump, after which nothing is read.
func TestParseKernelRoutes(t *testing.T) {
	names := map[uint32]string{1: "lo", 2: "eth1", 3: "eth2"}
	msgs := []syscall.NetlinkMessage{
		routeMessage(syscall.AF_INET6, 0, 254, 186, 1, rtattr{syscall.RTA_PRIORITY, u32(20)}, rtattr{rtaMultipath, append(
			nexthop(0, 3, rtattr{syscall.RTA_GATEWAY, ip("fe80::98fc:f9ff:fe86:8a05")}),
			nexthop(1, 2, rtattr{syscall.RTA_GATEWAY, ip("fe80::a43c:43ff:fe37:125d")})...)}),
		routeMessage(syscall.AF_INET, 32, 254, 186, 1, rtattr{syscall.RTA_DST, ip("192.0.2.1")}, rtattr{rtaMultipath, append(
			nexthop(0, 3, rtattr{rtaVia, viaIPv6("fe80::98fc:f9ff:fe86:8a05")}),
			nexthop(0, 2, rtattr{rtaVia, viaIPv6("fe80::a43c:43ff:fe37:125d")})...)}),
		routeMessage(syscall.AF_INET6, 48, 254, 3, 6, rtattr{syscall.RTA_DST, ip("2001:db8:2::")}, rtattr{syscall.RTA_OIF, u32(1)}, rtattr{syscall.RTA_PRIORITY, u32(1024)}),
		routeMessage(syscall.AF_INET6, 64, 254, 2, 1, rtattr{syscall.RTA_DST, ip("fe80::")}, rtattr{syscall.RTA_OIF, u32(2)}, rtattr{syscall.RTA_PRIORITY, u32(256)}),
		routeMessage(syscall.AF_INET, 24, 252, 4, 1, rtattr{syscall.RTA_DST, ip("198.51.100.0")}, rtattr{rtaTable, u32(1000)}, rtattr{syscall.RTA_GATEWAY, ip("192.0.2.254")}, rtattr{syscall.RTA_OIF, u32(2)}),
		{Header: syscall.NlMsghdr{Type: syscall.NLMSG_DONE}},
		routeMessage(syscall.AF_INET, 0, 254, 16, 1),
	}
	routes, err := parseRoutes(msgs, names)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"::/0 unicast bgp table=254 metric=20 fe80::98fc:f9ff:fe86:8a05@eth2/1,fe80::a43c:43ff:fe37:125d@eth1/2",
		"192.0.2.1/32 unicast bgp table=254 metric=0 fe80::98fc:f9ff:fe86:8a05@eth2/1,fe80::a43c:43ff:fe37:125d@eth1/1",
		"2001:db8:2::/48 blackhole boot table=254 metric=1024 @lo/0",
		"fe80::/64 unicast kernel table=254 metric=256 @eth1/0",
		"198.51.100.0/24 unicast static table=1000 metric=0 192.0.2.254@eth1/0",
	}
	if got := describeKernel(routes); !slices.Equal(got, want) {
		t.Errorf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The node's own routes come from netlink: every network namespace has its loopback's.
func TestKernelRoutes(t *testing.T) {
	routes, err := KernelRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(routes, func(r *nodev1.KernelRoute) bool {
		return r.Destination == "127.0.0.1/32" && r.Type == "local" && r.Table == 255
	}) {
		t.Errorf("no local route of 127.0.0.1 in %v", describeKernel(routes))
	}
}
