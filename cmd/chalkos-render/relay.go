package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/services"
)

// The output dhcp-relay: the config of dnsmasq's DHCP relay of each VRF whose links DHCPRelay
// documents relay, which forwards the DHCPv4 and DHCPv6 requests of the clients on the links to
// the relays' servers through the VRF's table. The validator router checks what the kind's rules
// cannot, with derive.Model.Relays: one VRF per relay, the router's address of each family the
// servers have on each link, which the servers tell the link by, and that dnsmasq's sockets bind
// beside Kea's. The set check has refused a link that two relays name.

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
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	relays, err := d.Relays()
	if err != nil {
		return err
	}
	byVRF := map[string]*relayVRF{}
	for _, r := range relays {
		v := byVRF[r.VRF]
		if v == nil {
			v = &relayVRF{}
			byVRF[r.VRF] = v
		}
		for _, l := range r.Links {
			v.links = append(v.links, l.Link)
			for _, v6 := range []bool{false, true} {
				for _, s := range r.Servers[v6] {
					v.lines = append(v.lines, "dhcp-relay="+l.Local[v6].String()+","+s.String())
				}
			}
		}
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
		if err := writeFile(out, services.Relay.File(vrf), []byte(b.String())); err != nil {
			return err
		}
	}
	return nil
}
