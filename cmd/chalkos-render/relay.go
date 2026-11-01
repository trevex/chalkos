package main

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
)

// The output dhcp-relay: the config of dnsmasq's DHCP relay of each VRF whose links DHCPRelay
// documents relay, which forwards the DHCPv4 and DHCPv6 requests of the clients on the links to
// the relays' servers through the VRF's table. The renderer checks what the kind's rules cannot:
// one VRF per relay, one relay per link, the router's address of each family the servers have on
// each link, which the servers tell the link by, and that dnsmasq's sockets bind beside Kea's.

// relayFile is the file renderDHCPRelay writes for the default VRF's relay; a VRF's gets
// dnsmasq-relay-<vrf>.conf.
const relayFile = "dnsmasq-relay.conf"

// relayFileOf is the config file of the relay of vrf.
func relayFileOf(vrf string) string {
	if vrf == defaultVRF {
		return relayFile
	}
	return "dnsmasq-relay-" + vrf + ".conf"
}

// relayWords end the link refusals of a relay.
var relayWords = linkWords{"a relay names each link it relays on", "a relay relays in one VRF"}

// relayVRF is the part of a relay's config of one VRF: the links it relays on and its dhcp-relay
// lines, each the router's address on a link and a server.
type relayVRF struct {
	links []string
	lines []string
}

// renderDHCPRelay writes the config of dnsmasq's relay of each VRF whose links DHCPRelay documents
// relay, out/dnsmasq-relay.conf for the default VRF and out/dnsmasq-relay-<vrf>.conf for a VRF. A
// node without a DHCPRelay gets no file and runs no relay.
func renderDHCPRelay(in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	byKind, _ := in["documents"].(map[string]any)
	units, err := networkUnits(d.VRFs, byKind)
	if err != nil {
		return err
	}
	devices := vrfDevices(d)
	relayed := map[string]string{}
	// families are the relays that relay each family in each VRF, by Kea's server of the same.
	families := map[keaServer]string{}
	byVRF := map[string]*relayVRF{}
	for _, r := range d.DHCPRelays {
		links, vrf, err := subnetLinks(d, devices, r.Spec.Interfaces, relayWords)
		if err != nil {
			return fmt.Errorf("DHCPRelay %s: %w", r.Name, err)
		}
		servers, err := relayServers(r.Spec.Servers)
		if err != nil {
			return fmt.Errorf("DHCPRelay %s: %w", r.Name, err)
		}
		v := byVRF[vrf]
		if v == nil {
			v = &relayVRF{}
			byVRF[vrf] = v
		}
		for _, l := range links {
			if other, ok := relayed[l]; ok {
				return fmt.Errorf("DHCPRelay %s and DHCPRelay %s both relay %s; a link takes one relay", other, r.Name, l)
			}
			relayed[l] = r.Name
			v.links = append(v.links, l)
			network, err := linkNetwork(units, l, "its addresses")
			if err != nil {
				return fmt.Errorf("DHCPRelay %s: %w", r.Name, err)
			}
			for _, v6 := range []bool{false, true} {
				fam := servers[v6]
				if len(fam) == 0 {
					continue
				}
				families[keaServer{vrf: vrf, v6: v6}] = r.Name
				local, err := relayAddress(d, network, l, v6)
				if err != nil {
					return fmt.Errorf("DHCPRelay %s: %w", r.Name, err)
				}
				for _, s := range fam {
					v.lines = append(v.lines, "dhcp-relay="+local.String()+","+s.String())
				}
			}
		}
	}
	if err := relaysBesideServers(d, devices, families); err != nil {
		return err
	}
	vrfs := slices.Collect(maps.Keys(byVRF))
	slices.SortFunc(vrfs, func(a, b string) int { return strings.Compare(vrfOrder(a), vrfOrder(b)) })
	for _, vrf := range vrfs {
		v := byVRF[vrf]
		var b strings.Builder
		// bind-dynamic gives the sockets of ports 67 and 547 SO_REUSEADDR, without which the
		// default VRF's relay, whose sockets no device binds, and a VRF's relay of the same
		// family do not bind beside each other. Unlike bind-interfaces, it waits for links
		// that do not exist yet.
		fmt.Fprintf(&b, "# dnsmasq's DHCP relay of the VRF %s, rendered by chalkos-render. It relays the requests of\n"+
			"# the clients on its links to the servers, and serves neither DHCP nor DNS.\n"+
			"port=0\nbind-dynamic\nno-resolv\nno-hosts\nquiet-dhcp\nquiet-dhcp6\n", vrf)
		slices.Sort(v.links)
		for _, l := range v.links {
			b.WriteString("interface=" + l + "\n")
		}
		for _, l := range v.lines {
			b.WriteString(l + "\n")
		}
		if err := writeFile(out, relayFileOf(vrf), []byte(b.String())); err != nil {
			return err
		}
	}
	return nil
}

// notUnicast are the ranges DHCPRelay's rule refuses servers of, which a relay neither forwards
// to nor from.
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

// relayServers are a relay's servers by family, IPv6 under true: unicast addresses, once each.
func relayServers(servers []string) (map[bool][]netip.Addr, error) {
	out := map[bool][]netip.Addr{}
	for _, s := range servers {
		a, err := netip.ParseAddr(s)
		if err != nil || !relayUnicast(a) {
			return nil, fmt.Errorf("the server %q is no unicast address a relay forwards to", s)
		}
		if slices.Contains(out[a.Is6()], a) {
			return nil, fmt.Errorf("the server %s is named more than once", a)
		}
		out[a.Is6()] = append(out[a.Is6()], a)
	}
	return out, nil
}

// relayAddress is the router's first static address of a family on the link, which the Network
// network gives it: the relay forwards a request from it, and the server picks the subnet of the
// request's link by it, so a DHCPv6 relay takes a global one.
func relayAddress(d routerDocuments, network, link string, v6 bool) (netip.Addr, error) {
	for _, a := range d.Addresses {
		addr := a.prefix.Addr()
		if a.network == network && addr.Is6() == v6 && relayUnicast(addr) {
			return addr, nil
		}
	}
	family := "IPv4"
	if v6 {
		family = "global IPv6"
	}
	return netip.Addr{}, fmt.Errorf("Network %s gives %s no static %s address, which the relay forwards its clients' requests from", network, link, family)
}

// relaysBesideServers refuses a family that DHCPRelay documents relay and DHCPSubnet documents
// serve in one VRF, or in two VRFs one of which is the default: dnsmasq's and Kea's sockets of a
// family bind beside each other only when each is bound to its own VRF's device. families are the
// relays of each family in each VRF.
func relaysBesideServers(d routerDocuments, devices []string, families map[keaServer]string) error {
	for _, s := range d.DHCPSubnets {
		p, err := netip.ParsePrefix(s.Spec.Subnet)
		if err != nil {
			continue
		}
		// The output kea refuses a subnet of links it cannot place.
		_, vrf, err := subnetLinks(d, devices, s.Spec.Interfaces, subnetWords)
		if err != nil {
			continue
		}
		v6 := p.Addr().Is6() && !p.Addr().Is4In6()
		keys := slices.SortedFunc(maps.Keys(families), func(a, b keaServer) int {
			return strings.Compare(vrfOrder(a.vrf)+" "+a.family(), vrfOrder(b.vrf)+" "+b.family())
		})
		for _, k := range keys {
			relay := families[k]
			if k.v6 != v6 || (k.vrf != vrf && k.vrf != defaultVRF && vrf != defaultVRF) {
				continue
			}
			version := 4
			if v6 {
				version = 6
			}
			return fmt.Errorf("DHCPRelay %s relays DHCPv%d in VRF %s and DHCPSubnet %s serves DHCPv%d in VRF %s; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one", relay, version, k.vrf, s.Name, version, vrf)
		}
	}
	return nil
}
