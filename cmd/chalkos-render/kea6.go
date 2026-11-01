package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strings"
)

// The DHCPv6 part of the output kea: Kea's DHCPv6 server of each VRF whose links IPv6 DHCPSubnet
// documents serve. Kea answers DHCPv6 on UDP sockets of each link, and selects a subnet of a
// client on a link by the link the subnet names, so an IPv6 subnet takes one link. Clients ask
// the server when the link's router advertisements tell them to, which the link's Network sends.

// specialIPv6 are the ranges no IPv6 subnet or delegated prefix is taken from.
var specialIPv6 = []netip.Prefix{netip.MustParsePrefix("::/8"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8")}

// special reports whether p overlaps a range of specialIPv6.
func special(p netip.Prefix) bool {
	for _, s := range specialIPv6 {
		if s.Overlaps(p) {
			return true
		}
	}
	return false
}

// serveSubnet6 checks the IPv6 subnet s of the range p and finds its link, its VRF and the
// router's addresses in it.
func serveSubnet6(d routerDocuments, units []networkUnit, devices []string, s named[dhcpSubnetSpec], p netip.Prefix) (servedSubnet, error) {
	sub := servedSubnet{name: s.Name, spec: s.Spec, prefix: p.Masked()}
	if p.Bits() > 120 || special(sub.prefix) {
		return sub, fmt.Errorf("the subnet %s is no IPv6 range of 120 bits at most outside ::/8, fe80::/10 and ff00::/8", sub.prefix)
	}
	sub.id = subnetID6(sub.prefix)
	var err error
	if sub.links, sub.vrf, err = subnetLinks(d, devices, s.Spec.Interfaces, subnetWords); err != nil {
		return sub, err
	}
	if len(sub.links) != 1 {
		return sub, fmt.Errorf("an IPv6 subnet is served on one link, the one Kea's subnet names; give each of %s a DHCPSubnet of its own", strings.Join(sub.links, ", "))
	}
	if s.Spec.Gateway != nil {
		return sub, fmt.Errorf("gateway is for IPv4 subnets; IPv6 clients learn their routers from router advertisements")
	}
	if s.Spec.Boot != nil {
		return sub, fmt.Errorf("boot is for IPv4 subnets")
	}
	own, err := ownAddresses(d, units, sub, "which routes the subnet's addresses to the link")
	if err != nil {
		return sub, err
	}
	var taken []takenAddress
	for _, o := range own {
		taken = append(taken, takenAddress{o, "the router's own address on the subnet's link"})
	}
	for _, a := range s.Spec.DNS {
		addr, err := netip.ParseAddr(a)
		if err != nil || !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
			return sub, fmt.Errorf("the DNS server %q is no IPv6 address", a)
		}
		taken = append(taken, takenAddress{addr, "a DNS server"})
	}
	// inSubnet parses an address of a pool or reservation, which lies in the subnet and is neither
	// its first address, the subnet routers' anycast address, nor taken.
	inSubnet := func(what, a string) (netip.Addr, error) {
		addr, err := netip.ParseAddr(a)
		if err != nil || !addr.Is6() || addr.Zone() != "" || !sub.prefix.Contains(addr) {
			return addr, fmt.Errorf("%s, %q, is no address of the subnet %s", what, a, sub.prefix)
		}
		if addr == sub.prefix.Addr() {
			return addr, fmt.Errorf("%s, %s, is the subnet routers' anycast address of the subnet %s", what, addr, sub.prefix)
		}
		for _, t := range taken {
			if t.addr == addr {
				return addr, fmt.Errorf("%s, %s, is %s", what, addr, t.what)
			}
		}
		return addr, nil
	}
	for _, pool := range s.Spec.Pools {
		first, err := inSubnet("the pool's first address", pool.First)
		if err != nil {
			return sub, err
		}
		last, err := inSubnet("the pool's last address", pool.Last)
		if err != nil {
			return sub, err
		}
		if last.Less(first) {
			return sub, fmt.Errorf("the pool %s-%s ends before it starts", first, last)
		}
		for _, t := range taken {
			if !t.addr.Less(first) && !last.Less(t.addr) {
				return sub, fmt.Errorf("the pool %s-%s holds %s, %s", first, last, t.addr, t.what)
			}
		}
		for _, other := range sub.pools {
			if !last.Less(other[0]) && !other[1].Less(first) {
				return sub, fmt.Errorf("the pools %s-%s and %s-%s overlap", other[0], other[1], first, last)
			}
		}
		sub.pools = append(sub.pools, [2]netip.Addr{first, last})
	}
	for _, r := range s.Spec.Reservations {
		if !macAddress.MatchString(r.MAC) {
			return sub, fmt.Errorf("%q is no MAC address in lower case", r.MAC)
		}
		a, err := inSubnet("the reservation of "+r.MAC, r.Address)
		if err != nil {
			return sub, err
		}
		for _, pool := range sub.pools {
			if !a.Less(pool[0]) && !pool[1].Less(a) {
				return sub, fmt.Errorf("the reservation of %s, %s, lies in the pool %s-%s; reserve addresses outside the pools", r.MAC, a, pool[0], pool[1])
			}
		}
		if r.Hostname != nil && !dhcpName.MatchString(*r.Hostname) {
			return sub, fmt.Errorf("the reservation of %s: %q is no host name", r.MAC, *r.Hostname)
		}
	}
	if pd := s.Spec.PrefixDelegation; pd != nil {
		r, err := netip.ParsePrefix(pd.Prefix)
		if err != nil || !r.Addr().Is6() || r.Addr().Is4In6() || special(r.Masked()) {
			return sub, fmt.Errorf("prefixDelegation's prefix %q is no IPv6 range outside ::/8, fe80::/10 and ff00::/8", pd.Prefix)
		}
		if pd.Length <= r.Bits() || pd.Length > 128 {
			return sub, fmt.Errorf("prefixDelegation's length %d is not longer than its prefix %s, nor 128 at most", pd.Length, r.Masked())
		}
		if r.Overlaps(sub.prefix) {
			return sub, fmt.Errorf("prefixDelegation's prefix %s overlaps the subnet %s", r.Masked(), sub.prefix)
		}
		sub.delegated, sub.length = r.Masked(), pd.Length
		// A route of a delegated prefix would take the router's own address on a link of the VRF
		// away from the link.
		for _, a := range d.Addresses {
			vrf, ok := d.Membership.Networks[a.network]
			if !ok {
				vrf = defaultVRF
			}
			if vrf == sub.vrf && sub.delegated.Contains(a.prefix.Addr()) {
				return sub, fmt.Errorf("prefixDelegation's prefix %s holds %s, the address Network %s gives a link of VRF %s", sub.delegated, a.prefix.Addr(), a.network, vrf)
			}
		}
	}
	if dom := s.Spec.Domain; dom != nil && !dhcpName.MatchString(*dom) {
		return sub, fmt.Errorf("%q is no domain", *dom)
	}
	if sub.seconds, err = leaseSeconds(s.Spec.LeaseTime); err != nil {
		return sub, err
	}
	return sub, advertised(d, units, sub)
}

// advertised refuses a subnet whose link's Network sends no router advertisements that tell its
// clients to ask the DHCPv6 server: with Managed=yes for addresses, which pools and reservations
// lease, and with Managed=yes or OtherInformation=yes for delegated prefixes and options alone.
// networkd sends advertisements on a link whose [Network] has IPv6SendRA=yes, with the flags of
// its [IPv6SendRA] section, and reads the former names IPv6PrefixDelegation= and
// [IPv6PrefixDelegation] as well.
func advertised(d routerDocuments, units []networkUnit, sub servedSubnet) error {
	link := sub.links[0]
	network, err := linkNetwork(units, link, "its router advertisements")
	if err != nil {
		return err
	}
	spec := d.Networks[network]
	// The networkd output writes sections and keys in name order, the former names first.
	send := unitBool(spec, [2]string{"network", "ipv6PrefixDelegation"}, [2]string{"network", "ipv6SendRA"})
	managed := unitBool(spec, [2]string{"ipv6PrefixDelegation", "managed"}, [2]string{"ipv6SendRA", "managed"})
	other := unitBool(spec, [2]string{"ipv6PrefixDelegation", "otherInformation"}, [2]string{"ipv6SendRA", "otherInformation"})
	addresses := len(sub.pools) > 0 || len(sub.spec.Reservations) > 0
	switch {
	case addresses && (!send || !managed):
		return fmt.Errorf("Network %s sends no router advertisements on %s with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA", network, link)
	case !send || (!managed && !other):
		return fmt.Errorf("Network %s sends no router advertisements on %s with Managed=yes or OtherInformation=yes, which tell its clients to ask the DHCPv6 server; give it ipv6SendRA: true in network and otherInformation: true in ipv6SendRA", network, link)
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
		for _, s := range oneOrMany(spec[k[0]]) {
			sec, _ := s.(map[string]any)
			for _, v := range oneOrMany(sec[k[1]]) {
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
		text = strings.ToLower(strings.Trim(v, unitWhitespace))
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

// networkSpecs are the specs of the Network documents of byKind, by name.
func networkSpecs(byKind map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := documentName("Network", doc)
		if err != nil {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		out[name] = spec
	}
	return out
}

// subnetID6 is Kea's number for the IPv6 subnet p: FNV-1a of its address and length, which
// renderKea refuses should two subnets of a server share it.
func subnetID6(p netip.Prefix) uint32 {
	h := fnv.New32a()
	a := p.Addr().As16()
	h.Write(a[:])
	h.Write([]byte{byte(p.Bits())})
	return keaSubnetID(h.Sum32())
}

// keaSubnetID is the number of Kea's range, 1 to 4294967294, nearest to sum.
func keaSubnetID(sum uint32) uint32 {
	return min(max(sum, 1), 4294967294)
}

// kea6Config is the config of the DHCPv6 server srv: UDP sockets on the subnets' links, its
// control socket with the lease commands, its leases in a file, the hook that records the
// delegated prefixes for the router to route, when a subnet delegates any, and the subnets.
func kea6Config(srv keaServer, subnets []servedSubnet, pdHook string) map[string]any {
	instance := srv.instance()
	var links []string
	var subnet6 []any
	delegates := false
	for _, s := range subnets {
		links = appendNew(links, s.links[0])
		subnet6 = append(subnet6, kea6Subnet(s))
		delegates = delegates || s.delegated.IsValid()
	}
	hooks := []any{map[string]any{"library": "libdhcp_lease_cmds.so"}}
	if delegates {
		// Kea routes no prefix it delegates. The hook runs pdHook, which records each delegated
		// prefix and the link-local address that asked for it, for chalkd to route.
		hooks = append(hooks, map[string]any{"library": "libdhcp_run_script.so", "parameters": map[string]any{"name": pdHook}})
	}
	dhcp6 := map[string]any{
		"interfaces-config": map[string]any{
			"interfaces":                      links,
			"service-sockets-retry-wait-time": keaRetryWait,
			"service-sockets-max-retries":     keaRetries,
		},
		"control-sockets": []any{map[string]any{"socket-type": "unix", "socket-name": "/run/kea/" + instance + ".sock"}},
		"lease-database":  map[string]any{"type": "memfile", "persist": true, "name": "/var/lib/kea/" + instance + ".leases", "lfc-interval": 3600},
		"hooks-libraries": hooks,
		"subnet6":         subnet6,
		"loggers":         keaLoggers(srv),
	}
	return map[string]any{"Dhcp6": dhcp6}
}

// kea6Subnet is the subnet s as Kea's subnet6 takes it: its link, pools, delegated prefixes,
// options and reservations. Clients prefer their addresses for the lease's whole time, and renew
// at half of it.
func kea6Subnet(s servedSubnet) map[string]any {
	subnet := map[string]any{
		"id":                 s.id,
		"subnet":             s.prefix.String(),
		"interface":          s.links[0],
		"valid-lifetime":     s.seconds,
		"preferred-lifetime": s.seconds,
		"user-context":       map[string]any{"name": s.name},
	}
	var pools []any
	for _, p := range s.pools {
		pools = append(pools, map[string]any{"pool": p[0].String() + " - " + p[1].String()})
	}
	if pools != nil {
		subnet["pools"] = pools
	}
	if s.delegated.IsValid() {
		subnet["pd-pools"] = []any{map[string]any{
			"prefix":        s.delegated.Addr().String(),
			"prefix-len":    s.delegated.Bits(),
			"delegated-len": s.length,
		}}
	}
	var options []any
	if len(s.spec.DNS) > 0 {
		var dns []string
		for _, a := range s.spec.DNS {
			dns = append(dns, netip.MustParseAddr(a).String())
		}
		options = append(options, map[string]any{"name": "dns-servers", "data": strings.Join(dns, ", ")})
	}
	if s.spec.Domain != nil {
		options = append(options, map[string]any{"name": "domain-search", "data": *s.spec.Domain})
	}
	if options != nil {
		subnet["option-data"] = options
	}
	var reservations []any
	for _, r := range s.spec.Reservations {
		h := map[string]any{"hw-address": r.MAC, "ip-addresses": []string{netip.MustParseAddr(r.Address).String()}}
		if r.Hostname != nil {
			h["hostname"] = *r.Hostname
		}
		reservations = append(reservations, h)
	}
	if reservations != nil {
		subnet["reservations"] = reservations
	}
	return subnet
}
