package main

import (
	"hash/fnv"
	"net/netip"
	"strings"
)

// The DHCPv6 part of the output kea: Kea's DHCPv6 server of each VRF whose links IPv6 DHCPSubnet
// documents serve. Kea answers DHCPv6 on UDP sockets of each link, and selects a subnet of a
// client on a link by the link the subnet names, so an IPv6 subnet takes one link. Clients ask
// the server when the link's router advertisements tell them to, which the link's Network sends.

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
		links = appendNew(links, s.Links[0])
		subnet6 = append(subnet6, kea6Subnet(s))
		delegates = delegates || s.Delegated.IsValid()
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
		"control-sockets": []any{map[string]any{"socket-type": "unix", "socket-name": srv.socket()}},
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
		"subnet":             s.Prefix.String(),
		"interface":          s.Links[0],
		"valid-lifetime":     s.seconds,
		"preferred-lifetime": s.seconds,
		"user-context":       map[string]any{"name": s.Name},
	}
	var pools []any
	for _, p := range s.Pools {
		pools = append(pools, map[string]any{"pool": p[0].String() + " - " + p[1].String()})
	}
	if pools != nil {
		subnet["pools"] = pools
	}
	if s.Delegated.IsValid() {
		subnet["pd-pools"] = []any{map[string]any{
			"prefix":        s.Delegated.Addr().String(),
			"prefix-len":    s.Delegated.Bits(),
			"delegated-len": s.Length,
		}}
	}
	var options []any
	if len(s.Spec.DNS) > 0 {
		var dns []string
		for _, a := range s.Spec.DNS {
			dns = append(dns, netip.MustParseAddr(a).String())
		}
		options = append(options, map[string]any{"name": "dns-servers", "data": strings.Join(dns, ", ")})
	}
	if s.Spec.Domain != nil {
		options = append(options, map[string]any{"name": "domain-search", "data": *s.Spec.Domain})
	}
	if options != nil {
		subnet["option-data"] = options
	}
	var reservations []any
	for _, r := range s.Spec.Reservations {
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
