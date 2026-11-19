package main

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// chronyServerFile is the file renderChronyServer writes, which chrony's confdir reads.
const chronyServerFile = "server.conf"

// renderChronyServer writes the TimeServer's directives of chrony to out/server.conf: an allow
// line per client range, a rate limit per client and, with localStratum, the local reference in
// orphan mode. chrony has a bindaddress per family alone, so it answers NTP on every address and
// the firewall accepts it on the addresses of listen alone. A node without a TimeServer gets no
// file, and chrony answers no one.
func renderChronyServer(in map[string]any, out string) error {
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	switch len(d.TimeServers) {
	case 0:
		return nil
	case 1:
	default:
		return derive.InternalErrorf("the input holds %d TimeServer documents; a node has one", len(d.TimeServers))
	}
	s := d.TimeServers[0]
	_, allow, err := serviceAddresses("NTP", s.Spec.Listen, s.Spec.Allow)
	if err != nil {
		return derive.Within("TimeServer "+s.Name, err)
	}
	var b strings.Builder
	b.WriteString("# The node's TimeServer, rendered by chalkos-render chrony-server.\n")
	for _, p := range allow {
		fmt.Fprintf(&b, "allow %s\n", p)
	}
	// chrony answers a client once every two seconds on average, in bursts of 16 responses such as
	// iburst's, and drops the requests beyond, which ntp-clients shows as the client's drops.
	b.WriteString("ratelimit interval 1 burst 16\n")
	if l := s.Spec.LocalStratum; l != nil {
		if *l < 1 || *l > 15 {
			return derive.Within("TimeServer "+s.Name, derive.InternalErrorf("the local stratum %d is outside 1 to 15", *l))
		}
		fmt.Fprintf(&b, "local stratum %d orphan\n", *l)
	}
	return writeFile(out, chronyServerFile, []byte(b.String()))
}

// timeServerOpenings are NTP's UDP port 123 of each TimeServer, from its clients' ranges to the
// addresses it listens on, in the default VRF, where chrony runs.
func timeServerOpenings(servers []derive.Named[derive.TimeServerSpec]) ([]opening, error) {
	var out []opening
	for _, s := range servers {
		listen, allow, err := serviceAddresses("NTP", s.Spec.Listen, s.Spec.Allow)
		if err != nil {
			return nil, derive.Within("TimeServer "+s.Name, err)
		}
		o := opening{comment: "TimeServer " + s.Name, protocol: "udp", ports: []derive.PortRange{"123"}, sources: allow}
		for _, a := range listen {
			o.destinations = append(o.destinations, netip.PrefixFrom(a, a.BitLen()))
		}
		out = append(out, o)
	}
	return out, nil
}
