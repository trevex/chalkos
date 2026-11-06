package derive

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// The DHCPSubnet and DHCPRelay documents as the router serves and relays them: the links each
// names, their VRF, the router's addresses on them, and what a subnet delegates, which the
// documents give only together. The kinds' rules check each document on its own: a subnet's
// range, its pools and reservations against the range, each other and the gateway, DNS and boot
// servers, and its options, and a relay's servers. The model checks what spans documents: one VRF
// per subnet or relay, one subnet of each family per link, no overlap within a server, a static
// address of the router in the subnet on each link, kept out of the pools and reservations, what a
// subnet delegates against the router's addresses and routes, router advertisements that send
// IPv6 clients to the DHCPv6 server, the router's addresses a relay forwards from, and relays
// beside servers. Kea reads and answers DHCPv4 on raw sockets ahead of the firewall, so a document
// names each of its links, as the schemas' pattern, which takes no glob, holds it to.

// Subnet is a DHCPSubnet document with the links it is served on, their VRF and the router's
// addresses it was checked against.
type Subnet struct {
	Name string
	Spec DHCPSubnetSpec
	// Prefix is the subnet, masked, and Links the links it is served on, sorted, all in VRF.
	Prefix netip.Prefix
	Links  []string
	VRF    string
	// Pools are the first and the last address of each pool, in the document's order.
	Pools [][2]netip.Addr
	// Delegated is the range an IPv6 subnet delegates prefixes of length Length from, invalid for
	// one that delegates none.
	Delegated netip.Prefix
	Length    int
}

// Is6 tells whether Kea's DHCPv6 server serves the subnet. An IPv4-mapped range, which the rules
// refuse, is IPv6 to Kea, so the relays and the firewall take it as IPv6 too.
func (s Subnet) Is6() bool { return s.Prefix.Addr().Is6() }

// MaxDelegated is the number of delegated prefixes chalkd routes at most, pkg/router's
// MaxPDRecords: one record each, of all servers.
const MaxDelegated = 65536

// Subnets are the DHCPSubnet documents as the router serves them, in their order. It refuses,
// each subnet at its first fault, an interface that is a VRF's device or a NAT64's link, links of
// two VRFs, links in a VRF of a Network's VRF= that no VRF document declares, a link no Network
// matches by Name= alone or whose Network gives it no static address in the subnet, a pool or
// reservation that holds such an address, a delegated range that holds the router's address or
// route in the VRF or a NAT64's prefix of it, and an IPv6 subnet whose link sends no router
// advertisements that send its clients to the server. Of the subnets it accepts, it then refuses
// a link that two subnets of a family serve, subnets of a VRF and family that overlap, their
// delegated ranges included, and subnets that delegate more than MaxDelegated prefixes in all.
// It guards, with an internal error, what the schemas and rules refuse and would panic.
func (d Model) Subnets() ([]Subnet, error) {
	if len(d.DHCPSubnets) == 0 {
		return nil, nil
	}
	units, err := d.Units()
	if err != nil {
		return nil, err
	}
	devices := d.Devices()
	var subs []Subnet
	var errs []error
	for _, s := range d.DHCPSubnets {
		sub, err := d.serveSubnet(units, devices, s)
		if isInternal(err) {
			return nil, Within("DHCPSubnet "+s.Name, err)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		subs = append(subs, sub)
	}
	// served are the subnets of each link, by family.
	served := map[bool]map[string]string{false: {}, true: {}}
	// byServer are the subnets of each VRF and family.
	byServer := map[string][]Subnet{}
	for _, sub := range subs {
		for _, l := range sub.Links {
			if other, ok := served[sub.Is6()][l]; ok {
				errs = append(errs, refusals.Errorf("DHCPSubnet", sub.Name, interfacePath(sub.Spec.Interfaces, l), "DHCPSubnet %s and DHCPSubnet %s both serve %s; a link takes one subnet of each family", other, sub.Name, l))
				continue
			}
			served[sub.Is6()][l] = sub.Name
		}
		server := fmt.Sprintf("%s %t", sub.VRF, sub.Is6())
		for _, other := range byServer[server] {
			if err := apart(other, sub); err != nil {
				errs = append(errs, err)
			}
		}
		byServer[server] = append(byServer[server], sub)
	}
	errs = append(errs, delegableWithin(subs))
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return subs, nil
}

// apart refuses b, a subnet of the server of a, whose range overlaps a's, the ranges they
// delegate prefixes from included.
func apart(a, b Subnet) error {
	ranges := func(s Subnet) []netip.Prefix {
		if s.Delegated.IsValid() {
			return []netip.Prefix{s.Prefix, s.Delegated}
		}
		return []netip.Prefix{s.Prefix}
	}
	for _, p := range ranges(a) {
		for _, q := range ranges(b) {
			if p.Overlaps(q) {
				at := "spec.subnet"
				if q != b.Prefix {
					at = "spec.prefixDelegation.prefix"
				}
				return refusals.Errorf("DHCPSubnet", b.Name, at, "DHCPSubnet %s and DHCPSubnet %s overlap in VRF %s: %s and %s", a.Name, b.Name, a.VRF, p, q)
			}
		}
	}
	return nil
}

// serveSubnet finds the links of the subnet s, their VRF and the router's addresses in it, which
// no pool or reservation holds, and checks an IPv6 subnet's delegated prefixes and router
// advertisements.
func (d Model) serveSubnet(units []Unit, devices []string, s Named[DHCPSubnetSpec]) (Subnet, error) {
	sub := Subnet{Name: s.Name, Spec: s.Spec}
	p, err := netip.ParsePrefix(s.Spec.Subnet)
	if err != nil {
		return sub, InternalErrorf("the subnet %q is no address range", s.Spec.Subnet)
	}
	sub.Prefix = p.Masked()
	if sub.Links, sub.VRF, err = d.servedLinks(devices, "DHCPSubnet", s.Name, s.Spec.Interfaces, "a subnet is served in one VRF"); err != nil {
		return sub, err
	}
	why, what := "which Kea answers its clients from", "the router's own address on the subnet's links"
	if sub.Is6() {
		why, what = "which routes the subnet's addresses to the link", "the router's own address on the subnet's link"
	}
	own, err := d.ownAddresses(units, sub, why)
	if err != nil {
		return sub, err
	}
	if sub.Pools, err = ownApart(s, own, what); err != nil || !sub.Is6() {
		return sub, err
	}
	if pd := s.Spec.PrefixDelegation; pd != nil {
		// delegableWithin shifts by the bits a delegated prefix has beyond the range's, which a
		// length no longer than the range's makes negative, a shift Go panics on.
		r, err := netip.ParsePrefix(pd.Prefix)
		if err != nil || pd.Length <= r.Bits() {
			return sub, InternalErrorf("prefixDelegation's prefix %q is no range shorter than its length %d", pd.Prefix, pd.Length)
		}
		sub.Delegated, sub.Length = r.Masked(), pd.Length
		// A route of a delegated prefix would take the router's own address on a link of the VRF
		// away from the link.
		for _, a := range d.Addresses {
			if vrf := d.VRFOfNetwork(a.Network); vrf == sub.VRF && sub.Delegated.Contains(a.Prefix.Addr()) {
				return sub, refusals.Errorf("DHCPSubnet", sub.Name, "spec.prefixDelegation.prefix", "prefixDelegation's prefix %s holds %s, the address Network %s gives a link of VRF %s", sub.Delegated, a.Prefix.Addr(), a.Network, vrf)
			}
		}
		if err := d.delegatesNoRoute(sub); err != nil {
			return sub, err
		}
	}
	return sub, d.advertised(units, sub)
}

// servedLinks are the links the interfaces of a subnet or relay serve it on, sorted, and their
// VRF; oneVRF ends the refusal of links of two VRFs. It refuses an interface that is a VRF's
// device or a NAT64's link, links of two VRFs and links in a VRF of a Network's VRF= that no VRF
// document declares. It guards, with an internal error, no interface, as an IPv6 subnet's config
// names its first, and a name that is no interface's, which dnsmasq's config and the firewall's
// ruleset would read as syntax.
func (d Model) servedLinks(devices []string, kind, name string, interfaces []string, oneVRF string) ([]string, string, error) {
	if len(interfaces) == 0 {
		return nil, "", InternalErrorf("interfaces names no interface")
	}
	var links []string
	for j, i := range interfaces {
		if !PlainInterface.MatchString(i) {
			return nil, "", InternalErrorf("interfaces: %q is no interface name", i)
		}
		at := fmt.Sprintf("spec.interfaces[%d]", j)
		if slices.Contains(devices, i) {
			return nil, "", refusals.Errorf(kind, name, at, "%s is the device of VRF %s; name the VRF's links", i, i)
		}
		for _, n := range d.NAT64 {
			if i == NAT64LinkPrefix+n.Name {
				return nil, "", refusals.Errorf(kind, name, at, "%s is the link of NAT64 %s", i, n.Name)
			}
		}
		if !slices.Contains(links, i) {
			links = append(links, i)
		}
	}
	slices.Sort(links)
	vrf := ""
	for _, l := range links {
		v, err := d.Membership.LinkVRF(l)
		if err != nil {
			return nil, "", err
		}
		if vrf != "" && v != vrf {
			return nil, "", refusals.Errorf(kind, name, interfacePath(interfaces, l), "its links are in VRF %s and VRF %s; %s", vrf, v, oneVRF)
		}
		vrf = v
	}
	if vrf != DefaultVRF && !slices.ContainsFunc(d.VRFs, func(v Named[VRFSpec]) bool { return v.Name == vrf }) {
		return nil, "", refusals.Errorf(kind, name, "spec.interfaces", "its links are in the VRF %q, which no VRF document declares", vrf)
	}
	return links, vrf, nil
}

// interfacePath is the field of a subnet's or relay's interfaces that names the link, the first
// that does, as spec.interfaces[i].
func interfacePath(interfaces []string, link string) string {
	return fmt.Sprintf("spec.interfaces[%d]", max(slices.Index(interfaces, link), 0))
}

// linkNetwork is the name of the Network document networkd picks for the link, which the model
// reads the link's addresses and router advertisements from: one that matches it by Name= alone.
// Without one it refuses the field at path of the document kind/name, the subnet's or relay's
// interface that names the link; why says what the model reads of the Network.
func linkNetwork(units []Unit, kind, name, path, link, why string) (string, error) {
	cs := Candidates(units, link)
	if len(cs) == 0 || cs[0].Narrowed {
		return "", refusals.Errorf(kind, name, path, "no Network document matches %s by Name= alone, so the renderer cannot find %s; give the link one", link, why)
	}
	return cs[0].Name, nil
}

// ownAddresses are the router's static addresses in the subnet on its links, which the Network
// networkd picks for each link gives. Kea answers a DHCPv4 client on a link from the link's
// address in the subnet, and the address routes an IPv6 subnet's addresses to the link, so it
// refuses a link without one; why ends the refusal.
func (d Model) ownAddresses(units []Unit, sub Subnet, why string) ([]netip.Addr, error) {
	var own []netip.Addr
	for _, l := range sub.Links {
		at := interfacePath(sub.Spec.Interfaces, l)
		network, err := linkNetwork(units, "DHCPSubnet", sub.Name, at, l, "its address in the subnet "+sub.Prefix.String())
		if err != nil {
			return nil, err
		}
		found := false
		for _, a := range d.Addresses {
			if a.Network == network && sub.Prefix.Contains(a.Prefix.Addr()) {
				found = true
				if !slices.Contains(own, a.Prefix.Addr()) {
					own = append(own, a.Prefix.Addr())
				}
			}
		}
		if !found {
			return nil, refusals.Errorf("DHCPSubnet", sub.Name, at, "Network %s gives %s no static address in the subnet %s, %s", network, l, sub.Prefix, why)
		}
	}
	return own, nil
}

// ownApart are the pools of the subnet s, each its first and last address. It refuses a pool or
// reservation that holds one of the router's own addresses own on the subnet's links, which Kea
// would lease to a client; what says what they are. The rules have kept the pools and
// reservations inside the subnet and apart from each other and from the gateway and the DNS and
// boot servers. It guards, with an internal error, an address that does not parse.
func ownApart(s Named[DHCPSubnetSpec], own []netip.Addr, what string) ([][2]netip.Addr, error) {
	var pools [][2]netip.Addr
	for i, p := range s.Spec.Pools {
		first, err1 := netip.ParseAddr(p.First)
		last, err2 := netip.ParseAddr(p.Last)
		if err1 != nil || err2 != nil {
			return nil, InternalErrorf("the pool %q-%q is no range of addresses", p.First, p.Last)
		}
		for _, o := range own {
			if !o.Less(first) && !last.Less(o) {
				return nil, refusals.Errorf("DHCPSubnet", s.Name, fmt.Sprintf("spec.pools[%d]", i), "the pool %s-%s holds %s, %s", first, last, o, what)
			}
		}
		pools = append(pools, [2]netip.Addr{first, last})
	}
	for i, r := range s.Spec.Reservations {
		a, err := netip.ParseAddr(r.Address)
		if err != nil {
			return nil, InternalErrorf("the reservation of %s, %q, is no address", r.MAC, r.Address)
		}
		if slices.Contains(own, a) {
			return nil, refusals.Errorf("DHCPSubnet", s.Name, fmt.Sprintf("spec.reservations[%d].address", i), "the reservation of %s, %s, is %s", r.MAC, a, what)
		}
	}
	return pools, nil
}

// delegatesNoRoute refuses a delegated range of sub that holds a route the router makes in the
// subnet's VRF: a static route of a Network of the VRF's links, or a NAT64's prefix. A route of a
// delegated prefix would replace such a route of the same prefix and shadow a longer one; a
// route of a wider range is shadowed by it instead, as it should be.
func (d Model) delegatesNoRoute(sub Subnet) error {
	for _, network := range slices.Sorted(maps.Keys(d.Networks)) {
		vrf := d.VRFOfNetwork(network)
		if vrf != sub.VRF {
			continue
		}
		for _, s := range OneOrMany(d.Networks[network]["route"]) {
			sec, _ := s.(map[string]any)
			for _, v := range OneOrMany(sec["destination"]) {
				text, _ := v.(string)
				text = strings.Trim(text, UnitWhitespace)
				p, err := netip.ParsePrefix(text)
				if err != nil {
					a, err := netip.ParseAddr(text)
					if err != nil {
						continue
					}
					p = netip.PrefixFrom(a, a.BitLen())
				}
				if p.Addr().Is6() && p.Bits() >= sub.Delegated.Bits() && p.Overlaps(sub.Delegated) {
					return refusals.Errorf("DHCPSubnet", sub.Name, "spec.prefixDelegation.prefix", "prefixDelegation's prefix %s holds the route of %s that Network %s gives a link of VRF %s", sub.Delegated, p.Masked(), network, vrf)
				}
			}
		}
	}
	for _, n := range d.NAT64 {
		vrf := VRFOf(n.Spec.VRF)
		p, err := netip.ParsePrefix(n.Spec.Prefix)
		if err != nil || vrf != sub.VRF {
			continue
		}
		if p.Bits() >= sub.Delegated.Bits() && p.Overlaps(sub.Delegated) {
			return refusals.Errorf("DHCPSubnet", sub.Name, "spec.prefixDelegation.prefix", "prefixDelegation's prefix %s holds NAT64 %s's prefix %s of VRF %s", sub.Delegated, n.Name, p.Masked(), vrf)
		}
	}
	return nil
}

// advertised refuses a subnet whose link's Network sends no router advertisements that tell its
// clients to ask the DHCPv6 server: with Managed=yes for addresses, which pools and reservations
// lease, and with Managed=yes or OtherInformation=yes for delegated prefixes and options alone.
// networkd sends advertisements on a link whose [Network] has IPv6SendRA=yes, with the flags of
// its [IPv6SendRA] section, and reads the former names IPv6PrefixDelegation= and
// [IPv6PrefixDelegation] as well.
func (d Model) advertised(units []Unit, sub Subnet) error {
	link := sub.Links[0]
	at := interfacePath(sub.Spec.Interfaces, link)
	network, err := linkNetwork(units, "DHCPSubnet", sub.Name, at, link, "its router advertisements")
	if err != nil {
		return err
	}
	spec := d.Networks[network]
	// The networkd output writes sections and keys in name order, the former names first.
	send := unitBool(spec, [2]string{"network", "ipv6PrefixDelegation"}, [2]string{"network", "ipv6SendRA"})
	managed := unitBool(spec, [2]string{"ipv6PrefixDelegation", "managed"}, [2]string{"ipv6SendRA", "managed"})
	other := unitBool(spec, [2]string{"ipv6PrefixDelegation", "otherInformation"}, [2]string{"ipv6SendRA", "otherInformation"})
	addresses := len(sub.Pools) > 0 || len(sub.Spec.Reservations) > 0
	switch {
	case addresses && (!send || !managed):
		return refusals.Errorf("DHCPSubnet", sub.Name, at, "Network %s sends no router advertisements on %s with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA", network, link)
	case !send || (!managed && !other):
		return refusals.Errorf("DHCPSubnet", sub.Name, at, "Network %s sends no router advertisements on %s with Managed=yes or OtherInformation=yes, which tell its clients to ask the DHCPv6 server; give it ipv6SendRA: true in network and otherInformation: true in ipv6SendRA", network, link)
	}
	return nil
}

// unitBool is the boolean that keys, each a section and a key of a Network document's spec, give
// in the unit in their order, as systemd-networkd reads it: the last value that parses as a
// boolean, of every section of the name, or false, the default of the keys it reads; systemd
// ignores a value that does not parse.
func unitBool(spec map[string]any, keys ...[2]string) bool {
	b := false
	for _, k := range keys {
		for _, s := range OneOrMany(spec[k[0]]) {
			sec, _ := s.(map[string]any)
			for _, v := range OneOrMany(sec[k[1]]) {
				if parsed, ok := systemdBool(v); ok {
					b = parsed
				}
			}
		}
	}
	return b
}

// systemdBool is a value of a unit's key as systemd's parse_boolean reads it, and whether it
// parses: 1, yes, y, true, t and on, or 0, no, n, false, f and off, in any case, once systemd has
// stripped the spaces around it.
func systemdBool(v any) (bool, bool) {
	var text string
	switch v := v.(type) {
	case bool:
		return v, true
	case json.Number:
		text = v.String()
	case string:
		text = strings.ToLower(strings.Trim(v, UnitWhitespace))
	default:
		return false, false
	}
	switch text {
	case "1", "yes", "y", "true", "t", "on":
		return true, true
	case "0", "no", "n", "false", "f", "off":
		return false, true
	}
	return false, false
}

// delegableWithin refuses subnets that delegate more prefixes altogether than chalkd routes. It
// counts the subnets in the order of their names and refuses the first whose prefixes take the
// count beyond the most, so that the refusal names the same subnet each time.
func delegableWithin(subs []Subnet) error {
	all := slices.Clone(subs)
	slices.SortFunc(all, func(a, b Subnet) int { return strings.Compare(a.Name, b.Name) })
	total := 0
	for _, s := range all {
		if !s.Delegated.IsValid() {
			continue
		}
		bits := s.Length - s.Delegated.Bits()
		if bits > 16 {
			total = MaxDelegated + 1
		} else {
			total += 1 << bits
		}
		if total > MaxDelegated {
			return refusals.Errorf("DHCPSubnet", s.Name, "spec.prefixDelegation.length", "the DHCPSubnet documents delegate more than %d prefixes, the most the router routes; delegate fewer", MaxDelegated)
		}
	}
	return nil
}

// Relay is a DHCPRelay document as dnsmasq's relay of its VRF runs it: its links, sorted, and on
// each the router's address of each family the relay has servers of, which it forwards the
// clients' requests from.
type Relay struct {
	Name  string
	VRF   string
	Links []RelayLink
	// Servers are the relay's servers of each family, IPv6 under true.
	Servers map[bool][]netip.Addr
}

// RelayLink is a link of a relay and the router's address on it that the relay forwards the
// requests of each family from, IPv6 under true: its first static address of the family that
// the Network networkd picks for the link gives, a global one for IPv6, as the server picks the
// subnet of the request's link by it.
type RelayLink struct {
	Link  string
	Local map[bool]netip.Addr
}

// Relays are the DHCPRelay documents as dnsmasq relays them, in their order. It refuses, each
// relay at its first fault, an interface that Subnets would refuse of a subnet, a link no Network
// matches by Name= alone, and a link whose Network gives it no static address of a family the
// relay has servers of. It then refuses a family that relays and subnets serve in one VRF, or in
// two VRFs one of which is the default: dnsmasq's and Kea's sockets of a family bind beside each
// other only when each is bound to its own VRF's device. It guards, with an internal error, a
// server that is no address without a zone: netip takes a zone of any characters, a line break
// among them, which dnsmasq's config would read as another option.
func (d Model) Relays() ([]Relay, error) {
	if len(d.DHCPRelays) == 0 {
		return nil, nil
	}
	units, err := d.Units()
	if err != nil {
		return nil, err
	}
	devices := d.Devices()
	var relays []Relay
	var errs []error
	for _, r := range d.DHCPRelays {
		relay, err := d.relay(units, devices, r)
		if isInternal(err) {
			return nil, Within("DHCPRelay "+r.Name, err)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		relays = append(relays, relay)
	}
	errs = append(errs, d.relaysBesideServers(devices, relays))
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return relays, nil
}

// relay is the DHCPRelay r as dnsmasq relays it.
func (d Model) relay(units []Unit, devices []string, r Named[DHCPRelaySpec]) (Relay, error) {
	relay := Relay{Name: r.Name, Servers: map[bool][]netip.Addr{}}
	for _, s := range r.Spec.Servers {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			return relay, InternalErrorf("the server %q is no address without a zone", s)
		}
		relay.Servers[a.Is6()] = append(relay.Servers[a.Is6()], a)
	}
	links, vrf, err := d.servedLinks(devices, "DHCPRelay", r.Name, r.Spec.Interfaces, "a relay relays in one VRF")
	if err != nil {
		return relay, err
	}
	relay.VRF = vrf
	for _, l := range links {
		at := interfacePath(r.Spec.Interfaces, l)
		network, err := linkNetwork(units, "DHCPRelay", r.Name, at, l, "its addresses")
		if err != nil {
			return relay, err
		}
		link := RelayLink{Link: l, Local: map[bool]netip.Addr{}}
		for _, v6 := range []bool{false, true} {
			if len(relay.Servers[v6]) == 0 {
				continue
			}
			if link.Local[v6], err = d.relayAddress(r.Name, at, network, l, v6); err != nil {
				return relay, err
			}
		}
		relay.Links = append(relay.Links, link)
	}
	return relay, nil
}

// notUnicast are the ranges a relay neither forwards to nor from, which DHCPRelay's rule refuses
// servers of.
var notUnicast = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::/8"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

// relayUnicast reports whether a is an address a relay forwards to or from: no zone, and outside
// notUnicast, which holds the IPv4-mapped IPv6 addresses too.
func relayUnicast(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	for _, p := range notUnicast {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// relayAddress is the router's first static address of a family on the link, which the Network
// network gives it. A link without one refuses the DHCPRelay called relay at path, the field that
// names the link.
func (d Model) relayAddress(relay, path, network, link string, v6 bool) (netip.Addr, error) {
	for _, a := range d.Addresses {
		addr := a.Prefix.Addr()
		if a.Network == network && addr.Is6() == v6 && relayUnicast(addr) {
			return addr, nil
		}
	}
	family := "IPv4"
	if v6 {
		family = "global IPv6"
	}
	return netip.Addr{}, refusals.Errorf("DHCPRelay", relay, path, "Network %s gives %s no static %s address, which the relay forwards its clients' requests from", network, link, family)
}

// relaysBesideServers refuses each subnet of a family that the relays relay in its VRF, or in the
// default VRF or the subnet's in the default VRF: dnsmasq's and Kea's sockets of a family bind
// beside each other only when each is bound to its own VRF's device. A subnet whose links
// servedLinks refuses is Subnets' to refuse.
func (d Model) relaysBesideServers(devices []string, relays []Relay) error {
	// families are the last relay that relays each family in each VRF, by VRF and family.
	type server struct {
		vrf string
		v6  bool
	}
	families := map[server]string{}
	for _, r := range relays {
		for _, v6 := range []bool{false, true} {
			if len(r.Servers[v6]) > 0 {
				families[server{r.VRF, v6}] = r.Name
			}
		}
	}
	// The default VRF first, then the VRFs by name, IPv4 before IPv6.
	key := func(s server) string {
		vrf := s.vrf
		if vrf == DefaultVRF {
			vrf = ""
		}
		return fmt.Sprintf("%s %t", vrf, s.v6)
	}
	order := slices.SortedFunc(maps.Keys(families), func(a, b server) int { return strings.Compare(key(a), key(b)) })
	var errs []error
	for _, s := range d.DHCPSubnets {
		p, err := netip.ParsePrefix(s.Spec.Subnet)
		if err != nil {
			continue
		}
		_, vrf, err := d.servedLinks(devices, "DHCPSubnet", s.Name, s.Spec.Interfaces, "")
		if err != nil {
			continue
		}
		v6 := p.Addr().Is6()
		for _, k := range order {
			if k.v6 != v6 || (k.vrf != vrf && k.vrf != DefaultVRF && vrf != DefaultVRF) {
				continue
			}
			version := 4
			if v6 {
				version = 6
			}
			errs = append(errs, refusals.Errorf("DHCPSubnet", s.Name, "spec.interfaces", "DHCPRelay %s relays DHCPv%d in VRF %s and DHCPSubnet %s serves DHCPv%d in VRF %s; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one", families[k], version, k.vrf, s.Name, version, vrf))
			break
		}
	}
	return errors.Join(errs...)
}
