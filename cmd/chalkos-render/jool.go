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

// The prefixes of the name of a NAT64's link in its VRF, n64-<name>, and of the files of the link's
// .network of networkd, 00-n64-<name>.
const (
	nat64LinkPrefix = "n64-"
	nat64UnitPrefix = "00-n64-"
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

// poolSet is the anonymous nftables set of the prefixes of n's explicit pool.
func (n nat64Instance) poolSet() string {
	pools := make([]string, len(n.pools))
	for i, p := range n.pools {
		pools[i] = p.String()
	}
	return "{ " + strings.Join(pools, ", ") + " }"
}

// nat64Instances are the NAT64 documents of d as chalkos-nat64 runs them. It refuses what
// joolInstances refuses and two names whose hashes pick one link. The rules refuse a pool entry
// that holds a static address of a Network document and a static address in nat64Range.
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
		table, err := mustGet(tables, "VRF", vrf)
		if err != nil {
			return nil, within("NAT64 "+doc.Name, err)
		}
		// The link's file names the default VRF by null, as chalkos-nat64 reads it.
		var linkVRF *string
		if vrf != defaultVRF {
			linkVRF = &vrf
		}
		slot := nat64Slot(doc.Name)
		outer, inner := nat64LinkAddresses(slot)
		if o, ok := taken[slot]; ok {
			return nil, refuse("NAT64", doc.Name, "metadata.name", "NAT64 %s and NAT64 %s both take the link addresses %s, which the hashes of their names pick; rename NAT64 %s", doc.Name, o, outer.Masked(), doc.Name)
		}
		taken[slot] = doc.Name
		n := nat64Instance{name: doc.Name, vrf: vrf, jool: jools[i], inner: inner.Addr(), link: nat64Link{
			VRF: linkVRF, Table: table, Outer: outer.String(), Inner: inner.String(), Routes: []string{},
		}}
		if len(n.jool.Pool4) == 0 {
			own := netip.PrefixFrom(n.inner, 32).String()
			for _, proto := range joolProtocols {
				n.jool.Pool4 = append(n.jool.Pool4, joolPool4{Protocol: strings.ToUpper(proto), Prefix: own, PortRange: ownPorts})
			}
		} else {
			// Each prefix of the pool once, as the entries of its protocols repeat it.
			for _, e := range n.jool.Pool4 {
				p := netip.MustParsePrefix(e.Prefix)
				if !slices.Contains(n.pools, p) {
					n.pools = append(n.pools, p)
					n.link.Routes = append(n.link.Routes, p.String())
				}
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

// joolInstances are Jool's instances of the NAT64 documents, an empty pool4 left empty. They refuse
// two NAT64s of one VRF whose prefixes overlap and two NAT64s whose pools share an address, which
// the rules leave to the renderer, as they would compare the documents in pairs. They guard, with
// an internal error, what jool file check passes and the kernel refuses once chalkos-nat64 loads
// the instance: a name whose link is longer than an interface's name, and what nat64Prefix and
// poolEntryRange guard.
func joolInstances(docs []named[nat64Spec]) ([]joolInstance, error) {
	var out []joolInstance
	// The prefixes, with their VRFs, and the pools' prefixes of the documents before the one at
	// hand.
	type vrfPrefix struct {
		name, vrf string
		prefix    netip.Prefix
	}
	var prefixes []vrfPrefix
	var pools []named[netip.Prefix]
	for _, n := range docs {
		what := "NAT64 " + n.Name
		// chalkos-nat64 creates the link n64-<name>, whose name the kernel refuses past 15
		// characters. decodeKind has refused every character an interface's name lacks.
		if link := nat64LinkPrefix + n.Name; !plainInterface.MatchString(link) {
			return nil, internalErrorf("%s: its link %s is longer than an interface name's 15 characters", what, link)
		}
		p, err := nat64Prefix(n.Spec.Prefix)
		if err != nil {
			return nil, within(what, err)
		}
		vrf := vrfOf(n.Spec.VRF)
		for _, o := range prefixes {
			if o.vrf == vrf && o.prefix.Overlaps(p) {
				return nil, refuse("NAT64", n.Name, "spec.prefix", "NAT64 %s and NAT64 %s both translate addresses of %s in VRF %s; give each NAT64 of a VRF a prefix of its own", n.Name, o.name, p, vrf)
			}
		}
		inst := joolInstance{Instance: n.Name, Framework: "netfilter", Global: joolGlobal{Pool6: p.String()}, Pool4: []joolPool4{}}
		var mine []netip.Prefix
		for i, e := range n.Spec.Pool4 {
			at := fmt.Sprintf("%s: pool entry %d", what, i+1)
			prefix, first, last, err := poolEntryRange(e)
			if err != nil {
				return nil, within(at, err)
			}
			for _, o := range pools {
				if o.Spec.Overlaps(prefix) {
					return nil, refuse("NAT64", n.Name, fmt.Sprintf("spec.pool4[%d]", i), "the entry shares addresses with the pool of NAT64 %s; give each NAT64 a pool of its own", o.Name)
				}
			}
			// An entry per protocol, in Jool's order. The schema takes no other protocol; an entry
			// of none would drop out, and a pool of no entries masquerade on the link's address.
			before := len(inst.Pool4)
			for _, proto := range joolProtocols {
				if slices.Contains(e.Protocols, proto) {
					inst.Pool4 = append(inst.Pool4, joolPool4{Protocol: strings.ToUpper(proto), Prefix: prefix.String(), PortRange: fmt.Sprintf("%d-%d", first, last)})
				}
			}
			if len(inst.Pool4) == before {
				return nil, internalErrorf("%s names no protocol Jool takes", at)
			}
			mine = append(mine, prefix)
		}
		prefixes = append(prefixes, vrfPrefix{n.Name, vrf, p})
		for _, x := range mine {
			pools = append(pools, named[netip.Prefix]{n.Name, x})
		}
		out = append(out, inst)
	}
	return out, nil
}

// poolEntryRange is the prefix, masked, and the first and last port of the pool entry e. It guards,
// with an internal error, a prefix that is no IPv4 range of 65536 addresses at most, which the
// firewall's sets and the link's routes take as IPv4 and for which Jool's kernel module would add
// an entry per address, and ports of the kernel's ephemeral range, 32768 to 60999, which the
// module refuses in a namespace that keeps that range.
func poolEntryRange(e poolEntry) (netip.Prefix, int, int, error) {
	p, err := netip.ParsePrefix(e.Prefix)
	if err != nil || !p.Addr().Is4() || p.Bits() < 16 {
		return netip.Prefix{}, 0, 0, internalErrorf("%q is no IPv4 range of 16 bits or more", e.Prefix)
	}
	first, last := e.Ports.bounds()
	if last >= ephemeralFirst && first <= ephemeralLast {
		return netip.Prefix{}, 0, 0, internalErrorf("the ports %s take the kernel's ephemeral ports, %d to %d, which Jool refuses", e.Ports, ephemeralFirst, ephemeralLast)
	}
	return p.Masked(), first, last, nil
}

// bounds are the first and last port of p, which UnmarshalJSON has read as a port or a range
// "<first>-<last>" from 1 to 65535 that runs forwards.
func (p portRange) bounds() (first, last int) {
	a, b, isRange := strings.Cut(string(p), "-")
	if !isRange {
		b = a
	}
	first, _ = strconv.Atoi(a)
	last, _ = strconv.Atoi(b)
	return first, last
}

// nat64Prefix is the NAT64 or DNS64 prefix s, masked. It guards, with an internal error, a prefix
// that Jool's kernel module or Unbound refuses, and that Unbound's config would read as syntax: no
// IPv6 range, a length other than RFC 6052's, and bits 64 to 71 set. The rules refuse those, and
// prefixes of IPv4-mapped, link-local, multicast or unspecified addresses, first.
func nat64Prefix(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is6() || !slices.Contains(nat64Lengths, p.Bits()) || p.Masked().Addr().As16()[8] != 0 {
		return netip.Prefix{}, internalErrorf("the prefix %q is no IPv6 range of 32, 40, 48, 56, 64 or 96 bits whose bits 64 to 71 are zero", s)
	}
	return p.Masked(), nil
}
