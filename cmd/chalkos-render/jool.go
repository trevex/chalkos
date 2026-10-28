package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// joolNameMax is the longest instance name Jool takes.
const joolNameMax = 15

// The kernel's ephemeral ports, which the router's own connections take and Jool refuses in a
// pool.
const (
	ephemeralFirst = 32768
	ephemeralLast  = 60999
)

// nat64Lengths are the lengths RFC 6052 gives NAT64 prefixes, which Jool and Unbound take.
var nat64Lengths = []int{32, 40, 48, 56, 64, 96}

// joolProtocols are a pool's protocols, in the order an entry renders them.
var joolProtocols = []string{"tcp", "udp", "icmp"}

// joolInstance is a NAT64 document as jool file handle reads it.
type joolInstance struct {
	Instance  string      `json:"instance"`
	Framework string      `json:"framework"`
	Global    joolGlobal  `json:"global"`
	Pool4     []joolPool4 `json:"pool4"`
}

type joolGlobal struct {
	Pool6 string `json:"pool6"`
}

type joolPool4 struct {
	Protocol  string `json:"protocol"`
	Prefix    string `json:"prefix"`
	PortRange string `json:"port range"`
}

// renderJool writes Jool's file of each NAT64 document to out/<name>.json: an instance of the
// document's name in netfilter mode, with the document's prefix as its pool6 and its pool as its
// pool4, an entry per protocol. chalkos-nat64.service hands each file to jool file handle. A node
// without NAT64 documents gets no file and runs no instance.
func renderJool(in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	instances, err := joolInstances(d.NAT64)
	if err != nil {
		return err
	}
	for _, i := range instances {
		data, err := json.MarshalIndent(i, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(out, i.Instance+".json", append(data, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// poolRange is the addresses and ports of a pool entry, the entry's number, for one protocol.
type poolRange struct {
	entry       int
	protocol    string
	prefix      netip.Prefix
	first, last int
}

// joolInstances are Jool's instances of the NAT64 documents. They refuse what the schemas and
// rules refuse, and two entries of one pool that share an address, a protocol and a port.
func joolInstances(docs []named[nat64Spec]) ([]joolInstance, error) {
	var out []joolInstance
	// The prefixes and pools of the documents before the one at hand.
	var prefixes, pools []named[netip.Prefix]
	// The document before the one at hand whose empty pool takes the router's own addresses.
	ownAddresses := ""
	for _, n := range docs {
		what := "NAT64 " + n.Name
		if len(n.Name) > joolNameMax {
			return nil, fmt.Errorf("%s: Jool takes an instance's name of %d characters at most", what, joolNameMax)
		}
		p, err := nat64Prefix(n.Spec.Prefix)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		for _, o := range prefixes {
			if o.Spec.Overlaps(p) {
				return nil, fmt.Errorf("%s and NAT64 %s both translate addresses of %s", what, o.Name, p)
			}
		}
		if len(n.Spec.Pool4) == 0 {
			if ownAddresses != "" {
				return nil, fmt.Errorf("%s and NAT64 %s both take the router's own addresses, with an empty pool4", what, ownAddresses)
			}
			ownAddresses = n.Name
		}
		inst := joolInstance{Instance: n.Name, Framework: "netfilter", Global: joolGlobal{Pool6: p.String()}, Pool4: []joolPool4{}}
		var ranges []poolRange
		for i, e := range n.Spec.Pool4 {
			at := fmt.Sprintf("%s: pool entry %d", what, i+1)
			r, err := poolEntryRanges(i+1, e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", at, err)
			}
			for _, o := range pools {
				if o.Spec.Overlaps(r[0].prefix) {
					return nil, fmt.Errorf("%s shares addresses with the pool of NAT64 %s", at, o.Name)
				}
			}
			for _, x := range r {
				for _, y := range ranges {
					if x.protocol == y.protocol && x.prefix.Overlaps(y.prefix) && x.first <= y.last && y.first <= x.last {
						return nil, fmt.Errorf("%s shares %s addresses and ports with pool entry %d", at, x.protocol, y.entry)
					}
				}
				inst.Pool4 = append(inst.Pool4, joolPool4{Protocol: strings.ToUpper(x.protocol), Prefix: x.prefix.String(), PortRange: fmt.Sprintf("%d-%d", x.first, x.last)})
			}
			ranges = append(ranges, r...)
		}
		prefixes = append(prefixes, named[netip.Prefix]{n.Name, p})
		for _, x := range ranges {
			pools = append(pools, named[netip.Prefix]{n.Name, x.prefix})
		}
		out = append(out, inst)
	}
	return out, nil
}

// poolEntryRanges are the addresses and ports of pool entry n, one per protocol in Jool's order:
// an IPv4 range of 65536 addresses at most without unspecified, loopback, multicast or broadcast
// addresses, and ports from 1 to 65535 outside the kernel's ephemeral ports.
func poolEntryRanges(n int, e poolEntry) ([]poolRange, error) {
	p, err := netip.ParsePrefix(e.Prefix)
	if err != nil || !p.Addr().Is4() || p.Bits() < 16 {
		return nil, fmt.Errorf("%q is no IPv4 range of 16 bits or more", e.Prefix)
	}
	p = p.Masked()
	if a := p.Addr(); a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || p.Contains(netip.AddrFrom4([4]byte{255, 255, 255, 255})) {
		return nil, fmt.Errorf("%s holds addresses no connection leaves from", p)
	}
	first, last, err := portBounds(e.Ports)
	if err != nil {
		return nil, err
	}
	if last >= ephemeralFirst && first <= ephemeralLast {
		return nil, fmt.Errorf("the ports %s take the kernel's ephemeral ports, %d to %d, which Jool refuses", e.Ports, ephemeralFirst, ephemeralLast)
	}
	if len(e.Protocols) == 0 {
		return nil, fmt.Errorf("it names no protocol")
	}
	for i, proto := range e.Protocols {
		if !slices.Contains(joolProtocols, proto) {
			return nil, fmt.Errorf("%q is none of tcp, udp and icmp", proto)
		}
		if slices.Contains(e.Protocols[:i], proto) {
			return nil, fmt.Errorf("it names %s twice", proto)
		}
	}
	var out []poolRange
	for _, proto := range joolProtocols {
		if slices.Contains(e.Protocols, proto) {
			out = append(out, poolRange{entry: n, protocol: proto, prefix: p, first: first, last: last})
		}
	}
	return out, nil
}

// portBounds are the first and last port of a port or a range "<first>-<last>".
func portBounds(p portRange) (int, int, error) {
	first, last, isRange := strings.Cut(string(p), "-")
	if !isRange {
		last = first
	}
	a, errA := strconv.Atoi(first)
	b, errB := strconv.Atoi(last)
	if errA != nil || errB != nil || a < 1 || a > b || b > 65535 {
		return 0, 0, fmt.Errorf("the ports %q are no port or range from 1 to 65535", p)
	}
	return a, b, nil
}

// nat64Prefix is a NAT64 prefix as RFC 6052 gives them, masked: an IPv6 range of 32, 40, 48, 56,
// 64 or 96 bits of no link-local, multicast or unspecified addresses, whose bits 64 to 71 are
// zero.
func nat64Prefix(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() || !slices.Contains(nat64Lengths, p.Bits()) {
		return netip.Prefix{}, fmt.Errorf("the prefix %q is no IPv6 range of 32, 40, 48, 56, 64 or 96 bits", s)
	}
	p = p.Masked()
	if a := p.Addr(); a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return netip.Prefix{}, fmt.Errorf("the prefix %s holds link-local, multicast or unspecified addresses", p)
	}
	if p.Addr().As16()[8] != 0 {
		return netip.Prefix{}, fmt.Errorf("the prefix %s sets bits 64 to 71, which RFC 6052 keeps zero", p)
	}
	return p, nil
}
