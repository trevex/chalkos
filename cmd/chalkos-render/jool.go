package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// The names of a NAT64's link in its VRF, n64-<name>, and of its network namespace, nat64-<name>,
// and the files of the namespace's .network of networkd.
const (
	nat64LinkPrefix      = "n64-"
	nat64NamespacePrefix = "nat64-"
	nat64UnitPrefix      = "00-n64-"
)

// nat64Range holds a /30 per NAT64 for the ends of its veth pair: the outer one in the VRF and the
// inner one in the namespace, whose address an empty pool4 takes.
var nat64Range = netip.MustParsePrefix("169.254.64.0/18")

// nat64Slots are the /30s of nat64Range.
const nat64Slots = 1 << (30 - 18)

// ownPorts are the ports of the inner address that a NAT64 of an empty pool4 takes, those Jool
// takes of a node's own addresses; the firewall masquerades them within the same range.
const ownPorts = "61001-65535"

// mainTable is the kernel's main routing table, the default VRF's.
const mainTable = 254

// linkLocal4 are IPv4's link-local addresses, of which nat64Range is part.
var linkLocal4 = netip.MustParsePrefix("169.254.0.0/16")

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

// renderJool writes, for each NAT64 document, Jool's file to out/<name>.json, an instance of the
// document's name in netfilter mode with the document's prefix as its pool6 and its pool as its
// pool4, an entry per protocol, and its link's file to out/<name>.veth, which chalkos-nat64.service
// reads to join the instance's namespace to its VRF. A node without NAT64 documents gets no file
// and runs no instance.
func renderJool(in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	instances, err := nat64Instances(d)
	if err != nil {
		return err
	}
	for _, n := range instances {
		for _, f := range []struct {
			name string
			v    any
		}{{n.name + ".json", n.jool}, {n.name + ".veth", n.link}} {
			data, err := json.MarshalIndent(f.v, "", "  ")
			if err != nil {
				return err
			}
			if err := writeFile(out, f.name, append(data, '\n')); err != nil {
				return err
			}
		}
	}
	return nil
}

// nat64Link is a NAT64's file <name>.veth: the VRF of its link n64-<name>, null for the default
// VRF, and that VRF's table; the addresses of the link's outer end and of its inner end in the
// namespace; and the IPv4 prefixes routed to the inner end, those of an explicit pool4.
type nat64Link struct {
	VRF    *string  `json:"vrf"`
	Table  uint32   `json:"table"`
	Outer  string   `json:"outer"`
	Inner  string   `json:"inner"`
	Routes []string `json:"routes"`
}

// nat64Instance is a NAT64 as chalkos-nat64 runs it and the firewall sees it.
type nat64Instance struct {
	name, vrf string
	jool      joolInstance
	link      nat64Link
	// inner is the address of the link's inner end.
	inner netip.Addr
	// pools are the prefixes of an explicit pool4, none for an empty one.
	pools []netip.Prefix
}

// nat64Instances are the NAT64 documents of d as chalkos-nat64 runs them. It refuses what
// joolInstances refuses, a VRF no document declares, two names whose hashes pick one link, and a
// pool entry that holds a static address of a Network document: the router answers its own
// addresses before any route to the link.
func nat64Instances(d routerDocuments) ([]nat64Instance, error) {
	jools, err := joolInstances(d.NAT64)
	if err != nil {
		return nil, err
	}
	tables := map[string]uint32{defaultVRF: mainTable}
	for _, v := range d.VRFs {
		tables[v.Name] = v.Spec.Table
	}
	// The NAT64 of each slot taken.
	taken := map[int]string{}
	var out []nat64Instance
	for i, doc := range d.NAT64 {
		vrf := vrfOf(doc.Spec.VRF)
		table, ok := tables[vrf]
		if !ok {
			return nil, fmt.Errorf("NAT64 %s names the VRF %q, which no VRF document declares; leave vrf out for the default VRF", doc.Name, vrf)
		}
		slot := nat64Slot(doc.Name)
		outer, inner := nat64LinkAddresses(slot)
		if o, ok := taken[slot]; ok {
			return nil, fmt.Errorf("NAT64 %s and NAT64 %s both take the link addresses %s, which the hashes of their names pick; rename one of them", doc.Name, o, outer.Masked())
		}
		taken[slot] = doc.Name
		n := nat64Instance{name: doc.Name, vrf: vrf, jool: jools[i], inner: inner.Addr(), link: nat64Link{
			VRF: doc.Spec.VRF, Table: table, Outer: outer.String(), Inner: inner.String(), Routes: []string{},
		}}
		if len(n.jool.Pool4) == 0 {
			own := netip.PrefixFrom(n.inner, 32).String()
			for _, proto := range joolProtocols {
				n.jool.Pool4 = append(n.jool.Pool4, joolPool4{Protocol: strings.ToUpper(proto), Prefix: own, PortRange: ownPorts})
			}
		} else {
			for _, e := range n.jool.Pool4 {
				p := netip.MustParsePrefix(e.Prefix)
				if slices.Contains(n.pools, p) {
					continue
				}
				for _, a := range d.Addresses {
					if p.Contains(a.prefix.Addr()) {
						return nil, fmt.Errorf("NAT64 %s: the pool entry %s holds %s, an address Network %s gives a link; the router answers it itself, so leave pool4 empty to take the address of the link the connections leave through", doc.Name, p, a.prefix.Addr(), a.network)
					}
				}
				n.pools = append(n.pools, p)
				n.link.Routes = append(n.link.Routes, p.String())
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// nat64Slot is the /30 of nat64Range that the NAT64 name takes, that of the FNV-1a hash of the
// name alone: a NAT64 keeps its link's addresses, and with them the connections its pool's
// masquerade tracks, while other NAT64s come and go.
func nat64Slot(name string) int {
	h := fnv.New32a()
	h.Write([]byte(name))
	return int(h.Sum32() % nat64Slots)
}

// nat64LinkAddresses are the addresses of the outer and the inner end of the link of slot s, the
// first and second host of its /30.
func nat64LinkAddresses(s int) (outer, inner netip.Prefix) {
	base := nat64Range.Addr().As4()
	first := netip.AddrFrom4([4]byte(binary.BigEndian.AppendUint32(nil, binary.BigEndian.Uint32(base[:])+uint32(4*s))))
	return netip.PrefixFrom(first.Next(), 30), netip.PrefixFrom(first.Next().Next(), 30)
}

// poolRange is the addresses and ports of a pool entry, the entry's number, for one protocol.
type poolRange struct {
	entry       int
	protocol    string
	prefix      netip.Prefix
	first, last int
}

// joolInstances are Jool's instances of the NAT64 documents, an empty pool4 left empty. They refuse
// what the schemas and rules refuse, and two entries of one pool that share an address, a protocol
// and a port.
func joolInstances(docs []named[nat64Spec]) ([]joolInstance, error) {
	var out []joolInstance
	// The prefixes, with their VRFs, and the pools of the documents before the one at hand.
	type vrfPrefix struct {
		name, vrf string
		prefix    netip.Prefix
	}
	var prefixes []vrfPrefix
	var pools []named[netip.Prefix]
	for _, n := range docs {
		what := "NAT64 " + n.Name
		if link := nat64LinkPrefix + n.Name; !plainInterface.MatchString(link) {
			return nil, fmt.Errorf("%s: its link %s is no interface name of 15 characters; give the NAT64 a name of %d characters at most", what, link, 15-len(nat64LinkPrefix))
		}
		p, err := nat64Prefix(n.Spec.Prefix)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		vrf := vrfOf(n.Spec.VRF)
		for _, o := range prefixes {
			if o.vrf == vrf && o.prefix.Overlaps(p) {
				return nil, fmt.Errorf("%s and NAT64 %s both translate addresses of %s in VRF %s", what, o.name, p, vrf)
			}
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
		prefixes = append(prefixes, vrfPrefix{n.Name, vrf, p})
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
	if p.Overlaps(linkLocal4) {
		return nil, fmt.Errorf("%s holds link-local addresses, which the NAT64s' links take", p)
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
