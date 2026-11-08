package main

import (
	"hash/fnv"
	"net/netip"

	"github.com/trevex/chalkos/pkg/router/keaconf"
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
func kea6Config(srv keaServer, subnets []servedSubnet, pdHook string) keaconf.Config {
	var links []string
	var subnet6 []keaconf.Subnet6
	delegates := false
	for _, s := range subnets {
		links = appendNew(links, s.Links[0])
		subnet6 = append(subnet6, kea6Subnet(s))
		delegates = delegates || s.Delegated.IsValid()
	}
	var hooks []keaconf.Hook
	if delegates {
		// Kea routes no prefix it delegates. The hook runs pdHook, which records each delegated
		// prefix and the link-local address that asked for it, for chalkd to route. Kea runs it
		// without waiting, so a release may overtake its commit and leave a record, whose route
		// lasts until the lease would have ended, the subnet's lease time at most.
		hooks = append(hooks, keaconf.Hook{Library: "libdhcp_run_script.so", Parameters: &keaconf.HookParameters{Name: pdHook}})
	}
	return keaconf.Config{Dhcp6: &keaconf.Dhcp6{Server: keaServerConfig(srv, links, hooks), Subnet6: subnet6}}
}

// kea6Subnet is the subnet s as Kea's subnet6 takes it: its link, pools, delegated prefixes,
// options and reservations. Clients prefer their addresses for the lease's whole time, and renew
// at half of it.
func kea6Subnet(s servedSubnet) keaconf.Subnet6 {
	options := dnsOptions("dns-servers", s)
	if s.Spec.Domain != nil {
		options = append(options, keaconf.Option{Name: "domain-search", Data: *s.Spec.Domain})
	}
	subnet := keaconf.Subnet6{
		Subnet:            keaSubnet(s, options),
		Interface:         s.Links[0],
		PreferredLifetime: s.seconds,
	}
	if s.Delegated.IsValid() {
		subnet.PDPools = []keaconf.PDPool{{
			Prefix:       s.Delegated.Addr(),
			PrefixLen:    s.Delegated.Bits(),
			DelegatedLen: s.Length,
		}}
	}
	return subnet
}
