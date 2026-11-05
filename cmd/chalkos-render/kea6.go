package main

import (
	"encoding/json"
	"hash/fnv"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

// The DHCPv6 part of the output kea: Kea's DHCPv6 server of each VRF whose links IPv6 DHCPSubnet
// documents serve. Kea answers DHCPv6 on UDP sockets of each link, and selects a subnet of a
// client on a link by the link the subnet names, so an IPv6 subnet takes one link. Clients ask
// the server when the link's router advertisements tell them to, which the link's Network sends.

// serveSubnet6 finds the link of the IPv6 subnet s of the range p, its VRF and the router's
// addresses in it, which no pool or reservation holds, and checks the prefixes it delegates
// against the router's addresses and routes in the VRF and the link's router advertisements. The
// rules have checked the rest of the subnet, one link and the delegated range apart from the
// subnet among it.
func serveSubnet6(d derive.Model, units []derive.Unit, devices []string, s derive.Named[derive.DHCPSubnetSpec], p netip.Prefix) (servedSubnet, error) {
	sub := servedSubnet{name: s.Name, spec: s.Spec, prefix: p.Masked()}
	sub.id = subnetID6(sub.prefix)
	var err error
	if sub.links, sub.vrf, err = subnetLinks(d, devices, "DHCPSubnet", s.Name, s.Spec.Interfaces, subnetWords); err != nil {
		return sub, err
	}
	own, err := ownAddresses(d, units, sub, "which routes the subnet's addresses to the link")
	if err != nil {
		return sub, err
	}
	if sub.pools, err = ownApart(s, own, "the router's own address on the subnet's link"); err != nil {
		return sub, err
	}
	if pd := s.Spec.PrefixDelegation; pd != nil {
		// delegableWithin shifts by the bits a delegated prefix has beyond the range's, which a
		// length no longer than the range's makes negative, a shift Go panics on.
		r, err := netip.ParsePrefix(pd.Prefix)
		if err != nil || pd.Length <= r.Bits() {
			return sub, derive.InternalErrorf("prefixDelegation's prefix %q is no range shorter than its length %d", pd.Prefix, pd.Length)
		}
		sub.delegated, sub.length = r.Masked(), pd.Length
		// A route of a delegated prefix would take the router's own address on a link of the VRF
		// away from the link.
		for _, a := range d.Addresses {
			vrf, ok := d.Membership.Networks[a.Network]
			if !ok {
				vrf = derive.DefaultVRF
			}
			if vrf == sub.vrf && sub.delegated.Contains(a.Prefix.Addr()) {
				return sub, refusals.Errorf("DHCPSubnet", sub.name, "spec.prefixDelegation.prefix", "prefixDelegation's prefix %s holds %s, the address Network %s gives a link of VRF %s", sub.delegated, a.Prefix.Addr(), a.Network, vrf)
			}
		}
		if err := delegatesNoRoute(d, sub); err != nil {
			return sub, err
		}
	}
	if sub.seconds, err = leaseSeconds(s.Spec.LeaseTime); err != nil {
		return sub, err
	}
	return sub, advertised(d, units, sub)
}

// delegatesNoRoute refuses a delegated range of sub that holds a route the router makes in the
// subnet's VRF: a static route of a Network of the VRF's links, or a NAT64's prefix. A route of a
// delegated prefix would replace such a route of the same prefix and shadow a longer one; a
// route of a wider range is shadowed by it instead, as it should be.
func delegatesNoRoute(d derive.Model, sub servedSubnet) error {
	for _, network := range slices.Sorted(maps.Keys(d.Networks)) {
		vrf, ok := d.Membership.Networks[network]
		if !ok {
			vrf = derive.DefaultVRF
		}
		if vrf != sub.vrf {
			continue
		}
		for _, s := range derive.OneOrMany(d.Networks[network]["route"]) {
			sec, _ := s.(map[string]any)
			for _, v := range derive.OneOrMany(sec["destination"]) {
				text, _ := v.(string)
				text = strings.Trim(text, derive.UnitWhitespace)
				p, err := netip.ParsePrefix(text)
				if err != nil {
					a, err := netip.ParseAddr(text)
					if err != nil {
						continue
					}
					p = netip.PrefixFrom(a, a.BitLen())
				}
				if p.Addr().Is6() && p.Bits() >= sub.delegated.Bits() && p.Overlaps(sub.delegated) {
					return refusals.Errorf("DHCPSubnet", sub.name, "spec.prefixDelegation.prefix", "prefixDelegation's prefix %s holds the route of %s that Network %s gives a link of VRF %s", sub.delegated, p.Masked(), network, vrf)
				}
			}
		}
	}
	for _, n := range d.NAT64 {
		vrf := derive.DefaultVRF
		if n.Spec.VRF != nil {
			vrf = *n.Spec.VRF
		}
		p, err := netip.ParsePrefix(n.Spec.Prefix)
		if err != nil || vrf != sub.vrf {
			continue
		}
		if p.Bits() >= sub.delegated.Bits() && p.Overlaps(sub.delegated) {
			return refusals.Errorf("DHCPSubnet", sub.name, "spec.prefixDelegation.prefix", "prefixDelegation's prefix %s holds NAT64 %s's prefix %s of VRF %s", sub.delegated, n.Name, p.Masked(), vrf)
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
func advertised(d derive.Model, units []derive.Unit, sub servedSubnet) error {
	link := sub.links[0]
	at := interfacePath(sub.spec.Interfaces, link)
	network, err := linkNetwork(units, "DHCPSubnet", sub.name, at, link, "its router advertisements")
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
		return refusals.Errorf("DHCPSubnet", sub.name, at, "Network %s sends no router advertisements on %s with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA", network, link)
	case !send || (!managed && !other):
		return refusals.Errorf("DHCPSubnet", sub.name, at, "Network %s sends no router advertisements on %s with Managed=yes or OtherInformation=yes, which tell its clients to ask the DHCPv6 server; give it ipv6SendRA: true in network and otherInformation: true in ipv6SendRA", network, link)
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
		for _, s := range derive.OneOrMany(spec[k[0]]) {
			sec, _ := s.(map[string]any)
			for _, v := range derive.OneOrMany(sec[k[1]]) {
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
		text = strings.ToLower(strings.Trim(v, derive.UnitWhitespace))
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
		// prefix and the link-local address that asked for it, for chalkd to route. Kea runs it
		// without waiting, so a release may overtake its commit and leave a record, whose route
		// lasts until the lease would have ended, the subnet's lease time at most.
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

// maxDelegated is the number of delegated prefixes chalkd routes at most, pkg/router's
// MaxPDRecords: one record each, of all servers.
const maxDelegated = 65536

// delegableWithin refuses subnets that delegate more prefixes altogether than chalkd routes. It
// counts the subnets of all servers in the order of their names and refuses the first whose
// prefixes take the count beyond the most, so that the refusal names the same subnet each time.
func delegableWithin(byServer map[keaServer][]servedSubnet) error {
	var all []servedSubnet
	for _, subnets := range byServer {
		all = append(all, subnets...)
	}
	slices.SortFunc(all, func(a, b servedSubnet) int { return strings.Compare(a.name, b.name) })
	total := 0
	for _, s := range all {
		if !s.delegated.IsValid() {
			continue
		}
		bits := s.length - s.delegated.Bits()
		if bits > 16 {
			total = maxDelegated + 1
		} else {
			total += 1 << bits
		}
		if total > maxDelegated {
			return refusals.Errorf("DHCPSubnet", s.name, "spec.prefixDelegation.length", "the DHCPSubnet documents delegate more than %d prefixes, the most the router routes; delegate fewer", maxDelegated)
		}
	}
	return nil
}
