package derive

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"slices"

	"github.com/trevex/chalkos/pkg/refusals"
)

// NAT64LinkPrefix starts the name of a NAT64's link in its VRF, n64-<name>.
const NAT64LinkPrefix = "n64-"

// nat64Range holds a /30 per NAT64 for the ends of its veth pair: the outer one in the VRF and the
// inner one in the namespace, whose address an empty pool4 takes.
var nat64Range = netip.MustParsePrefix("169.254.64.0/18")

// nat64Slots are the /30s of nat64Range.
const nat64Slots = 1 << (30 - 18)

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

// PoolProtocols are the protocols of a pool entry, in the order Jool's entries take them.
var PoolProtocols = []string{"tcp", "udp", "icmp"}

// NAT64Instance is a NAT64 document as chalkos-nat64 runs it, Jool takes it and the firewall sees
// it: its link n64-<name> in its VRF, the addresses of the link's ends and its pools.
type NAT64Instance struct {
	Name, VRF string
	// Table is the routing table of the VRF.
	Table uint32
	// Prefix is the NAT64's prefix, masked: Jool's pool6.
	Prefix netip.Prefix
	// Pool4 are the entries of an explicit pool4, none for an empty one, which takes the inner
	// address.
	Pool4 []PoolRange
	// Outer and Inner are the addresses of the link's outer end, in the VRF, and of its inner end,
	// in the namespace, with the length of their /30.
	Outer, Inner netip.Prefix
	// Pools are the prefixes of an explicit pool4, each once, in the order of its entries.
	Pools []netip.Prefix
}

// PoolRange is an entry of a NAT64's pool4: its prefix, masked, its first and last port, and the
// protocols of the entry that Jool takes, in Jool's order.
type PoolRange struct {
	Prefix      netip.Prefix
	First, Last int
	Protocols   []string
}

// NAT64Instances are the NAT64 documents of d as chalkos-nat64 runs them. It refuses what
// nat64Pools refuses and each NAT64 whose name's hash picks the link of one before it, every one
// it finds. The rules refuse a pool entry that holds a static address of a Network document and
// a static address in nat64Range.
func (d Model) NAT64Instances() ([]NAT64Instance, error) {
	out, err := nat64Pools(d.NAT64)
	if err != nil {
		return nil, err
	}
	var errs []error
	tables := map[string]uint32{DefaultVRF: mainTable}
	for _, v := range d.VRFs {
		tables[v.Name] = v.Spec.Table
	}
	// The NAT64 of each slot taken.
	taken := map[int]string{}
	for i := range out {
		n := &out[i]
		table, err := MustGet(tables, "VRF", n.VRF)
		if err != nil {
			return nil, Within("NAT64 "+n.Name, err)
		}
		slot := nat64Slot(n.Name)
		n.Table = table
		n.Outer, n.Inner = nat64LinkAddresses(slot)
		if o, ok := taken[slot]; ok {
			errs = append(errs, refusals.Errorf("NAT64", n.Name, "metadata.name", "NAT64 %s and NAT64 %s both take the link addresses %s, which the hashes of their names pick; rename NAT64 %s", n.Name, o, n.Outer.Masked(), n.Name))
		} else {
			taken[slot] = n.Name
		}
		// Each prefix of the pool once, as several entries may share it.
		for _, e := range n.Pool4 {
			if !slices.Contains(n.Pools, e.Prefix) {
				n.Pools = append(n.Pools, e.Prefix)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
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

// nat64Pools are the NAT64 documents' names, VRFs, prefixes and pools, as Jool takes them. They
// refuse two NAT64s of one VRF whose prefixes overlap and two NAT64s whose pools share an address,
// which the rules leave to the model, as they would compare the documents in pairs, each pair they
// find. They guard, with an internal error, what jool file check passes and the kernel refuses
// once chalkos-nat64 loads the instance: a name whose link is longer than an interface's name, a
// pool entry of no protocol Jool takes, and what NAT64Prefix and poolEntryRange guard.
func nat64Pools(docs []Named[NAT64Spec]) ([]NAT64Instance, error) {
	var out []NAT64Instance
	var errs []error
	// The pools' prefixes of the documents before the one at hand.
	var pools []Named[netip.Prefix]
	for _, n := range docs {
		what := "NAT64 " + n.Name
		// chalkos-nat64 creates the link n64-<name>, whose name the kernel refuses past 15
		// characters. decodeKind has refused every character an interface's name lacks.
		if link := NAT64LinkPrefix + n.Name; !PlainInterface.MatchString(link) {
			return nil, InternalErrorf("%s: its link %s is longer than an interface name's 15 characters", what, link)
		}
		p, err := NAT64Prefix(n.Spec.Prefix)
		if err != nil {
			return nil, Within(what, err)
		}
		vrf := VRFOf(n.Spec.VRF)
		for _, o := range out {
			if o.VRF == vrf && o.Prefix.Overlaps(p) {
				errs = append(errs, refusals.Errorf("NAT64", n.Name, "spec.prefix", "NAT64 %s and NAT64 %s both translate addresses of %s in VRF %s; give each NAT64 of a VRF a prefix of its own", n.Name, o.Name, p, vrf))
			}
		}
		inst := NAT64Instance{Name: n.Name, VRF: vrf, Prefix: p}
		for i, e := range n.Spec.Pool4 {
			at := fmt.Sprintf("%s: pool entry %d", what, i+1)
			prefix, first, last, err := poolEntryRange(e)
			if err != nil {
				return nil, Within(at, err)
			}
			for _, o := range pools {
				if o.Spec.Overlaps(prefix) {
					errs = append(errs, refusals.Errorf("NAT64", n.Name, fmt.Sprintf("spec.pool4[%d]", i), "the entry shares addresses with the pool of NAT64 %s; give each NAT64 a pool of its own", o.Name))
				}
			}
			// The entry's protocols in Jool's order. The schema takes no other protocol; an entry
			// of none would drop out, and a pool of no entries masquerade on the link's address.
			r := PoolRange{Prefix: prefix, First: first, Last: last}
			for _, proto := range PoolProtocols {
				if slices.Contains(e.Protocols, proto) {
					r.Protocols = append(r.Protocols, proto)
				}
			}
			if len(r.Protocols) == 0 {
				return nil, InternalErrorf("%s names no protocol Jool takes", at)
			}
			inst.Pool4 = append(inst.Pool4, r)
		}
		for _, r := range inst.Pool4 {
			pools = append(pools, Named[netip.Prefix]{n.Name, r.Prefix})
		}
		out = append(out, inst)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// poolEntryRange is the prefix, masked, and the first and last port of the pool entry e. It guards,
// with an internal error, a prefix that is no IPv4 range of 65536 addresses at most, which the
// firewall's sets and the link's routes take as IPv4 and for which Jool's kernel module would add
// an entry per address, and ports of the kernel's ephemeral range, 32768 to 60999, which the
// module refuses in a namespace that keeps that range.
func poolEntryRange(e PoolEntry) (netip.Prefix, int, int, error) {
	p, err := netip.ParsePrefix(e.Prefix)
	if err != nil || !p.Addr().Is4() || p.Bits() < 16 {
		return netip.Prefix{}, 0, 0, InternalErrorf("%q is no IPv4 range of 16 bits or more", e.Prefix)
	}
	first, last := e.Ports.bounds()
	if last >= ephemeralFirst && first <= ephemeralLast {
		return netip.Prefix{}, 0, 0, InternalErrorf("the ports %s take the kernel's ephemeral ports, %d to %d, which Jool refuses", e.Ports, ephemeralFirst, ephemeralLast)
	}
	return p.Masked(), first, last, nil
}

// NAT64Prefix is the NAT64 or DNS64 prefix s, masked. It guards, with an internal error, a prefix
// that Jool's kernel module or Unbound refuses, and that Unbound's config would read as syntax: no
// IPv6 range, a length other than RFC 6052's, and bits 64 to 71 set. The rules refuse those, and
// prefixes of IPv4-mapped, link-local, multicast or unspecified addresses, first.
func NAT64Prefix(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is6() || !slices.Contains(nat64Lengths, p.Bits()) || p.Masked().Addr().As16()[8] != 0 {
		return netip.Prefix{}, InternalErrorf("the prefix %q is no IPv6 range of 32, 40, 48, 56, 64 or 96 bits whose bits 64 to 71 are zero", s)
	}
	return p.Masked(), nil
}
