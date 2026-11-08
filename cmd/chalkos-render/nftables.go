package main

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// rulesetFile is the file renderRouterNftables writes.
const rulesetFile = "ruleset.nft"

// renderRouterNftables writes the router's firewall to out/ruleset.nft: the nftables table inet
// chalkos, which loading the file replaces in one transaction. port is chalkd's, which the input
// chain accepts on the management interfaces. With closed, as for the image's fallback ruleset,
// the forward chain drops what the protected rules do not accept.
func renderRouterNftables(port string, closed bool, in map[string]any, out string) error {
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 {
		return fmt.Errorf("%q is no TCP port of chalkd", port)
	}
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	fw, err := newFirewall(d, int(p), closed)
	if err != nil {
		return err
	}
	if fw.openings, err = bgpOpenings(d.Peers); err != nil {
		return err
	}
	dns, err := resolverOpenings(d.Resolvers)
	if err != nil {
		return err
	}
	fw.openings = append(fw.openings, dns...)
	dhcp, err := dhcpOpenings(d)
	if err != nil {
		return err
	}
	fw.openings = append(fw.openings, dhcp...)
	text, err := fw.ruleset()
	if err != nil {
		return err
	}
	return writeFile(out, rulesetFile, []byte(text))
}

// An opening is a port of one of the router's services, which the input chain accepts after the
// protected rules: from the interfaces and source ranges, and to the destination addresses, the
// service's documents scope it to, empty for any, with a comment that names the document.
type opening struct {
	comment    string
	protocol   derive.Protocol
	ports      []derive.PortRange
	interfaces []string
	// vrf is the VRF of the service, "" for the default VRF. The input hook sees a packet from a
	// VRF's link with the VRF's device as iifname and the link as sdifname.
	vrf          string
	sources      []netip.Prefix
	destinations []netip.Prefix
}

// bgpOpenings are the ports of each BGPPeer, on its interfaces, of its VRF, or from its address
// alone: BGP's TCP port 179 and, with bfd, BFD's UDP ports 3784 and 3785 of single-hop sessions,
// and 4784 of multi-hop ones from an address peer; a peer on a link is one hop away. A peer of a
// VRF is opened in its VRF alone. The BGPPeer's rules make a peer name one of address, interface
// and interfaces, and the validator router refuses a peer on a VRF's device.
func bgpOpenings(peers []derive.Named[derive.PeerSpec]) ([]opening, error) {
	var out []opening
	for _, p := range peers {
		what := "BGPPeer " + p.Name
		o := opening{comment: what}
		s := p.Spec
		switch {
		case s.Address != nil:
			a := *s.Address
			o.sources = []netip.Prefix{netip.PrefixFrom(a, a.BitLen())}
		case s.Interface != nil:
			o.interfaces = []string{*s.Interface}
		default:
			o.interfaces = s.Interfaces
		}
		vrf, err := derive.VRFName(s.VRF)
		if err != nil {
			return nil, derive.Within(what, err)
		}
		o.vrf = derive.VRFDevice(vrf)
		// An opening without interfaces and sources would accept the port from anywhere.
		if s.Address == nil {
			if err := checkInterfaces(what, o.interfaces); err != nil {
				return nil, err
			}
		}
		bgp := o
		bgp.protocol, bgp.ports = "tcp", []derive.PortRange{"179"}
		out = append(out, bgp)
		if s.BFD {
			bfd := o
			bfd.protocol, bfd.ports = "udp", []derive.PortRange{"3784", "3785"}
			if s.Address != nil {
				bfd.ports = append(bfd.ports, "4784")
			}
			out = append(out, bfd)
		}
	}
	return out, nil
}

// resolverOpenings are DNS's UDP and TCP port 53 of each Resolver, from its clients' ranges to the
// addresses it listens on, in its VRF alone; the default VRF's sockets take nothing from VRFs.
func resolverOpenings(resolvers []derive.Named[derive.ResolverSpec]) ([]opening, error) {
	var out []opening
	for _, r := range resolvers {
		what := "Resolver " + r.Name
		listen, allow, err := resolverAddresses(r.Spec)
		if err != nil {
			return nil, derive.Within(what, err)
		}
		o := opening{comment: what, ports: []derive.PortRange{"53"}}
		// The input hook sees a packet from a VRF's link with the VRF's device as iifname.
		vrf, err := derive.VRFName(r.Spec.VRF)
		if err != nil {
			return nil, derive.Within(what, err)
		}
		o.vrf = derive.VRFDevice(vrf)
		for _, a := range listen {
			o.destinations = append(o.destinations, netip.PrefixFrom(a, a.BitLen()))
		}
		o.sources = allow
		for _, proto := range []derive.Protocol{"udp", "tcp"} {
			x := o
			x.protocol = proto
			out = append(out, x)
		}
	}
	return out, nil
}

// firewall is what the ruleset is rendered from.
type firewall struct {
	router   derive.RouterSpec
	zones    map[string]zoneLinks
	sets     map[string]derive.AddressSetSpec
	setNames []string
	policies []derive.Named[derive.PolicySpec]
	nat      []derive.Named[derive.NATSpec]
	openings []opening
	// ctZones are the VRFs with a conntrack zone and links, by name.
	ctZones []ctZone
	// nat64 are the NAT64 instances, whose links the forward chain and source NAT treat apart.
	nat64 []derive.NAT64Instance
	// vrfDevices are the VRFs' devices, on which the strict reverse-path filter accepts
	// neighbour discovery from link-local sources, and the filter a marked packet's second pass.
	vrfDevices []string
	// tables are the VRF and NextHop documents' tables, which the PolicyRoute documents' targets
	// resolve to, targets the tables of the PolicyRoute documents, with their marks, and policy
	// the lines of the chain policy-routing.
	tables  derive.PolicyTables
	targets []routeTarget
	policy  []string
	port    int
	closed  bool
}

// ctZone is a VRF's conntrack zone and the links whose connections take it.
type ctZone struct {
	vrf   string
	zone  int
	links []string
}

func newFirewall(d derive.Model, port int, closed bool) (*firewall, error) {
	f := &firewall{
		router:   d.Router(),
		sets:     map[string]derive.AddressSetSpec{},
		policies: d.Policies,
		nat:      d.NAT,
		port:     port,
		closed:   closed,
	}
	devices := d.Devices()
	f.vrfDevices = devices
	// The validator router refuses a management interface that matches a VRF's device.
	if m := f.router.Management.Interfaces; m != nil {
		if err := checkInterfaces("Router: management.interfaces", m); err != nil {
			return nil, err
		}
	}
	zones, err := zoneLinksOf(d)
	if err != nil {
		return nil, err
	}
	f.zones = zones
	if f.nat64, err = d.NAT64Instances(); err != nil {
		return nil, err
	}
	for _, v := range d.VRFs {
		if v.Spec.ConntrackZone == nil {
			continue
		}
		z := *v.Spec.ConntrackZone
		links, err := d.Membership.Links(v.Name)
		if err != nil {
			return nil, err
		}
		// A NAT64's link carries the VRF's connections into and out of its namespace.
		for _, n := range f.nat64 {
			if n.VRF == v.Name {
				links = appendNew(links, derive.NAT64LinkPrefix+n.Name)
			}
		}
		if len(links) > 0 {
			f.ctZones = append(f.ctZones, ctZone{vrf: v.Name, zone: z, links: links})
		}
	}
	// The validator router refuses a NAT rule's interface that matches no link but a VRF's device
	// or a NAT64's link, which prerouting and postrouting never translate.
	for _, n := range d.NAT {
		for _, place := range natPlaces(n.Spec) {
			if place.Interface != nil {
				if err := checkInterfaces("NATRule "+n.Name, []string{*place.Interface}); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, s := range d.Sets {
		f.sets[s.Name] = s.Spec
		f.setNames = append(f.setNames, s.Name)
	}
	if f.tables, err = d.PolicyTables(); err != nil {
		return nil, err
	}
	if f.targets, err = policyTargets(d, f.tables); err != nil {
		return nil, err
	}
	if len(d.PolicyRoutes) > 0 {
		if f.policy, err = f.policyLines(d); err != nil {
			return nil, err
		}
	}
	// The rules refuse a policy from local, which no zone is: MustGet finds none.
	for _, p := range d.Policies {
		if p.Spec.To != "local" {
			if _, err := derive.MustGet(f.zones, "FirewallZone", p.Spec.To); err != nil {
				return nil, derive.Within("FirewallPolicy "+p.Name, err)
			}
		}
		if _, err := derive.MustGet(f.zones, "FirewallZone", p.Spec.From); err != nil {
			return nil, derive.Within("FirewallPolicy "+p.Name, err)
		}
	}
	return f, nil
}

// placeAt is a NATRule's place with its field's path in the document, which a refusal of the
// place names.
type placeAt struct {
	*derive.NATPlace
	path string
}

// natPlaces are the places a NATRule sets, in and out, with their paths.
func natPlaces(n derive.NATSpec) []placeAt {
	var out []placeAt
	if n.In != nil {
		out = append(out, placeAt{n.In, "spec.in"})
	}
	if n.Out != nil {
		out = append(out, placeAt{n.Out, "spec.out"})
	}
	return out
}

// zoneLinks are where the hooks see a zone's connections. The input and forward hooks see a
// packet from a VRF's link with the VRF's device as iifname and the link as sdifname; prerouting,
// postrouting and forward's oifname see the links themselves, and prerouting a packet from a
// VRF's link first with the link, the pass that decides its conntrack zone and translation.
type zoneLinks struct {
	// in are the zone's interfaces that may be links of no VRF, and the devices of its VRFs.
	in []string
	// sdif are the zone's interfaces that may be links of a VRF.
	sdif []string
	// links are the zone's interfaces and the links of its VRFs.
	links []string
}

// entries are the matches of the zone's connections in the input and forward hooks.
func (z zoneLinks) entries() []string {
	var out []string
	if len(z.in) > 0 {
		out = append(out, "iifname "+interfaceSet(z.in))
	}
	if len(z.sdif) > 0 {
		out = append(out, "meta sdifname "+interfaceSet(z.sdif))
	}
	return out
}

// zoneLinksOf finds where the hooks see each zone's connections. Without VRFs a zone's
// interfaces are its in and its links, as before VRFs. The validator router has refused an
// interface or glob that matches a VRF's device or a NAT64's link, and an interface that may be a
// link of a VRF another zone holds, so a VRF a zone holds is the zone's own, whose device covers
// its links. The uniqueness check refuses a VRF that two zones hold.
func zoneLinksOf(d derive.Model) (map[string]zoneLinks, error) {
	declared := map[string]bool{}
	for _, v := range d.VRFs {
		declared[v.Name] = true
	}
	held := map[string]bool{}
	for _, z := range d.Zones {
		for _, v := range z.Spec.VRFs {
			if _, err := derive.MustGet(declared, "VRF", v); err != nil {
				return nil, derive.Within("FirewallZone "+z.Name, err)
			}
			held[v] = true
		}
	}
	out := map[string]zoneLinks{}
	for _, z := range d.Zones {
		what := "FirewallZone " + z.Name
		if len(z.Spec.Interfaces) > 0 {
			if err := checkInterfaces(what, z.Spec.Interfaces); err != nil {
				return nil, err
			}
		}
		var zl zoneLinks
		for _, i := range z.Spec.Interfaces {
			vrfs, err := d.Membership.InterfaceVRFs(i)
			if err != nil {
				return nil, derive.Within(what, err)
			}
			for _, v := range vrfs {
				switch {
				case v == derive.DefaultVRF:
					zl.in = appendNew(zl.in, i)
				case !held[v]:
					zl.sdif = appendNew(zl.sdif, i)
				}
			}
			zl.links = appendNew(zl.links, i)
		}
		for _, v := range z.Spec.VRFs {
			links, err := d.Membership.Links(v)
			if err != nil {
				return nil, derive.Within(what, err)
			}
			zl.in = appendNew(zl.in, v)
			for _, l := range links {
				zl.links = appendNew(zl.links, l)
			}
		}
		out[z.Name] = zl
	}
	return out, nil
}

// appendNew appends s to l unless l holds it.
func appendNew(l []string, s string) []string {
	if slices.Contains(l, s) {
		return l
	}
	return append(l, s)
}

// The ICMP and ICMPv6 types the protected rules accept: errors, echo requests, and on IPv6
// neighbour discovery, router solicitations and advertisements and multicast listener discovery.
const (
	icmpErrors   = "destination-unreachable, time-exceeded, parameter-problem"
	icmpv6Errors = "destination-unreachable, packet-too-big, time-exceeded, parameter-problem"
	icmpv6Local  = icmpv6Errors + ", echo-request, mld-listener-query, mld-listener-report, mld-listener-done, nd-router-solicit, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert, mld2-listener-report"
)

// ruleset renders the table: the AddressSets, a chain per FirewallPolicy, the conntrack zones, the
// reverse-path filter, the chain policy-routing, input, forward and the NAT chains.
func (f *firewall) ruleset() (string, error) {
	var b strings.Builder
	b.WriteString("# The router's firewall, rendered by chalkos-render. Loading the file replaces the table\n" +
		"# inet chalkos in one transaction.\n" +
		"table inet chalkos\n" +
		"delete table inet chalkos\n" +
		"table inet chalkos {\n")
	for _, name := range f.setNames {
		s := f.sets[name]
		fmt.Fprintf(&b, "\tset addresses-%s {\n\t\ttype %s_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = %s\n\t}\n",
			name, s.Family, set(prefixes(s.Prefixes)))
	}
	for _, p := range f.policies {
		var lines []string
		for i, r := range p.Spec.Rules {
			rule, err := f.ruleLines(p.Name, i+1, r)
			if err != nil {
				return "", derive.Within(fmt.Sprintf("FirewallPolicy %s rule %d", p.Name, i+1), err)
			}
			lines = append(lines, rule...)
		}
		writeChain(&b, "policy-"+p.Name, "", append(lines, verdictOf(p.Spec.Default)))
	}
	f.conntrackZones(&b)
	f.prerouting(&b)
	f.policyRouting(&b)
	if err := f.input(&b); err != nil {
		return "", err
	}
	f.forward(&b)
	if err := f.translation(&b); err != nil {
		return "", err
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// writeChain writes the chain name with its lines. A base chain's header gives its type, hook and
// policy; a regular chain has the empty header.
func writeChain(b *strings.Builder, name, header string, lines []string) {
	b.WriteString("\tchain " + name + " {\n")
	if header != "" {
		b.WriteString("\t\t" + header + "\n")
	}
	for _, l := range lines {
		b.WriteString("\t\t" + l + "\n")
	}
	b.WriteString("\t}\n")
}

// conntrackZones puts the connections through the links of each VRF with a conntrack zone in its
// zone, at the raw priority before conntrack sees them. Prerouting sees a packet from a VRF's
// link first with the link as iifname, which decides its zone, and output a packet the router
// sends in a VRF first with the VRF's device, untracked, then with the link. A ruleset without
// zones has neither chain.
func (f *firewall) conntrackZones(b *strings.Builder) {
	if len(f.ctZones) == 0 {
		return
	}
	for _, c := range []struct{ name, hook, dir string }{{"ct-prerouting", "prerouting", "iifname"}, {"ct-output", "output", "oifname"}} {
		var lines []string
		for _, z := range f.ctZones {
			lines = append(lines, fmt.Sprintf("%s %s ct zone set %d comment %q", c.dir, interfaceSet(z.links), z.zone, "VRF "+z.vrf))
		}
		writeChain(b, c.name, "type filter hook "+c.hook+" priority raw; policy accept;", lines)
	}
}

// prerouting is the reverse-path filter, which NixOS's firewall applies on every other image.
// The kernel's fib expression passes ICMPv6 from :: to link-scope addresses: the neighbour
// solicitations of duplicate address detection and the MLD reports of an address not yet assigned.
// A VRF's link passes neighbour discovery through prerouting a second time, with the VRF's device
// as iif. There the strict check of a link-local source finds its route on the link, not on the
// device, and fails, so the strict filter accepts neighbour discovery from link-local sources on
// the VRFs' devices first. The hop limit of 255, which no router forwards, keeps it to the link.
func (f *firewall) prerouting(b *strings.Builder) {
	var fib string
	switch f.router.RPFilter {
	case "off":
		return
	case "loose":
		fib = "fib saddr . mark oif exists accept"
	case "strict":
		fib = "fib saddr . mark . iif oif exists accept"
	}
	var lines []string
	// A packet from a VRF's link passes prerouting a second time with the VRF's device. The
	// chain policy-routing marked it after the first pass, which the filter judged; judged again,
	// it would be judged by its policy's table.
	if len(f.policy) > 0 && len(f.vrfDevices) > 0 {
		lines = append(lines, fmt.Sprintf("iifname %s meta mark & 0x%08x != 0 accept", interfaceSet(f.vrfDevices), policyMarkMask))
	}
	lines = append(lines, "meta nfproto ipv4 udp sport . udp dport { 67 . 68, 68 . 67 } accept")
	if f.router.RPFilter == "strict" && len(f.vrfDevices) > 0 {
		lines = append(lines, "iifname "+interfaceSet(f.vrfDevices)+" ip6 saddr fe80::/10 ip6 hoplimit 255 "+
			"icmpv6 type { nd-router-solicit, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert } accept")
	}
	writeChain(b, "prerouting", "type filter hook prerouting priority mangle + 10; policy drop;", append(lines, fib))
}

// policyRouting is the chain policy-routing, after the reverse-path filter, which judges a packet
// by the table of the link it came in on: marked before, it would be judged by its policy's
// table. The routing decision follows every prerouting chain, so the mark decides it. A ruleset
// without PolicyRoute documents has no chain.
func (f *firewall) policyRouting(b *strings.Builder) {
	if len(f.policy) == 0 {
		return
	}
	writeChain(b, "policy-routing", "type filter hook prerouting priority mangle + 20; policy accept;", f.policy)
}

// input accepts the protected traffic, chalkd's port on the management interfaces, the openings,
// then hands each connection from a zone to the policy to local of that zone, and drops the rest.
func (f *firewall) input(b *strings.Builder) error {
	api := fmt.Sprintf("tcp dport %d accept", f.port)
	if f.router.Management.Interfaces != nil {
		api = "iifname " + interfaceSet(f.router.Management.Interfaces) + " " + api
	}
	lines := []string{
		`iifname "lo" accept`,
		"ct state established,related accept",
		"ct state invalid drop",
		"icmpv6 type { " + icmpv6Local + " } accept",
		"icmp type { " + icmpErrors + ", echo-request } accept",
		"ip6 daddr fe80::/64 udp sport 547 udp dport 546 accept",
		api,
	}
	for _, o := range f.openings {
		lines = append(lines, o.lines()...)
	}
	for _, p := range f.policies {
		if p.Spec.To == "local" {
			for _, from := range f.zones[p.Spec.From].entries() {
				lines = append(lines, from+" jump policy-"+p.Name)
			}
		}
	}
	writeChain(b, "input", "type filter hook input priority filter; policy drop;", lines)
	return nil
}

// forward accepts everything while no policy goes between two zones and the ruleset is not
// closed. Otherwise it accepts the protected traffic, the NAT64s' translated traffic into and out
// of their links, which no policy filters, and destination-translated connections, hands each
// connection to the policy of its pair of zones, and drops the rest, also from or to an interface
// in no zone. A policy to a zone of no link has no line.
func (f *firewall) forward(b *strings.Builder) {
	var between []derive.Named[derive.PolicySpec]
	for _, p := range f.policies {
		if p.Spec.To != "local" {
			between = append(between, p)
		}
	}
	if len(between) == 0 && !f.closed {
		writeChain(b, "forward", "type filter hook forward priority filter; policy accept;", nil)
		return
	}
	lines := []string{
		"ct state established,related accept",
		"ct state invalid drop",
		"icmpv6 type { " + icmpv6Errors + " } accept",
		"icmp type { " + icmpErrors + " } accept",
	}
	// A NAT64's link carries what Jool translated: out of it IPv6 from the prefix and IPv4 from
	// the pool or, for an own pool, the inner address; into it IPv6 to the prefix and IPv4 to an
	// explicit pool. An own pool's IPv4 comes back to the masqueraded connections alone. Anything
	// else on the link goes on to the zones, which hold no NAT64's link, and is dropped.
	for _, n := range f.nat64 {
		link, c := strconv.Quote(derive.NAT64LinkPrefix+n.Name), strconv.Quote("NAT64 "+n.Name)
		// The forward hook sees a packet from a VRF's link with the VRF's device as iifname.
		from := "iifname " + link
		if n.VRF != derive.DefaultVRF {
			from = "meta sdifname " + link
		}
		prefix := n.Prefix
		source := n.Inner.Addr().String()
		if len(n.Pools) > 0 {
			source = poolSet(n.Pools)
		}
		lines = append(lines,
			fmt.Sprintf("%s ip6 saddr %s accept comment %s", from, prefix, c),
			fmt.Sprintf("%s ip saddr %s accept comment %s", from, source, c),
			fmt.Sprintf("oifname %s ip6 daddr %s accept comment %s", link, prefix, c))
		if len(n.Pools) > 0 {
			lines = append(lines, fmt.Sprintf("oifname %s ip daddr %s accept comment %s", link, poolSet(n.Pools), c))
		}
	}
	if slices.ContainsFunc(f.nat, func(n derive.Named[derive.NATSpec]) bool { return n.Spec.Type == "dnat" }) {
		lines = append(lines, "ct status dnat accept")
	}
	for _, p := range between {
		to := f.zones[p.Spec.To].links
		if len(to) == 0 {
			continue
		}
		for _, from := range f.zones[p.Spec.From].entries() {
			lines = append(lines, fmt.Sprintf("%s oifname %s jump policy-%s", from, interfaceSet(to), p.Name))
		}
	}
	writeChain(b, "forward", "type filter hook forward priority filter; policy drop;", lines)
}

// translation renders the NATRule documents, in the order of their names: dnat in dstnat after the
// NAT64s' links and pools, masquerade and snat in srcnat after the NAT64s' lines. A ruleset without
// NAT rules and without an own pool of a NAT64 has neither chain.
func (f *firewall) translation(b *strings.Builder) error {
	var dst, src []string
	for _, n := range f.nat {
		line, err := f.natLine(n)
		if err != nil {
			return derive.Within("NATRule "+n.Name, err)
		}
		if n.Spec.Type == "dnat" {
			dst = append(dst, line)
		} else {
			src = append(src, line)
		}
	}
	// No rule translates the destination of a connection out of a NAT64's link, which Jool
	// translated already, or to its prefix or explicit pool, whose addresses belong to the NAT64
	// whole.
	if len(dst) > 0 {
		var exempt []string
		for _, n := range f.nat64 {
			c := strconv.Quote("NAT64 " + n.Name)
			exempt = append(exempt,
				fmt.Sprintf("iifname %s accept comment %s", strconv.Quote(derive.NAT64LinkPrefix+n.Name), c),
				fmt.Sprintf("ip6 daddr %s accept comment %s", n.Prefix, c))
			if len(n.Pools) > 0 {
				exempt = append(exempt, fmt.Sprintf("ip daddr %s accept comment %s", poolSet(n.Pools), c))
			}
		}
		dst = append(exempt, dst...)
	}
	if len(src) > 0 || slices.ContainsFunc(f.nat64, func(n derive.NAT64Instance) bool { return len(n.Pools) == 0 }) {
		src = append(f.nat64Lines(), src...)
	}
	for _, c := range []struct {
		name, hook, priority string
		lines                []string
	}{{"dstnat", "prerouting", "dstnat", dst}, {"srcnat", "postrouting", "srcnat", src}} {
		if len(c.lines) == 0 {
			continue
		}
		writeChain(b, c.name, "type nat hook "+c.hook+" priority "+c.priority+"; policy accept;", c.lines)
	}
	return nil
}

// nat64Lines are the source NAT lines of the NAT64s, ahead of the NATRules': no rule translates a
// connection into a NAT64's link, from its prefix, which IPv4 hosts open towards IPv6 through it,
// or from its explicit pool, and the main namespace masquerades an own pool's inner address to the
// address of the link a connection leaves through, within the ports Jool takes of it.
func (f *firewall) nat64Lines() []string {
	var lines []string
	for _, n := range f.nat64 {
		c := strconv.Quote("NAT64 " + n.Name)
		lines = append(lines,
			fmt.Sprintf("oifname %s accept comment %s", strconv.Quote(derive.NAT64LinkPrefix+n.Name), c),
			fmt.Sprintf("ip6 saddr %s accept comment %s", n.Prefix, c))
		if len(n.Pools) == 0 {
			lines = append(lines,
				fmt.Sprintf("ip saddr %s meta l4proto { tcp, udp } masquerade to :%s comment %s", n.Inner.Addr(), ownPorts, c),
				fmt.Sprintf("ip saddr %s masquerade comment %s", n.Inner.Addr(), c))
			continue
		}
		lines = append(lines, fmt.Sprintf("ip saddr %s accept comment %s", poolSet(n.Pools), c))
	}
	return lines
}

// natLine renders a NATRule. The NATRule's rules check its place, to, family and ports; natLine
// guards a rule without its place or to, and a zone without links, whose empty set
// nftables would not load. The validator router refuses that zone, a to.address in a NAT64's pool
// and a destination in a NAT64's prefix or pool.
func (f *firewall) natLine(doc derive.Named[derive.NATSpec]) (string, error) {
	n := doc.Spec
	family := familyWords[n.Family]
	parts := []string{"meta nfproto " + string(n.Family)}
	place, dir := n.Out, "oifname"
	if n.Type == "dnat" {
		place, dir = n.In, "iifname"
	}
	switch {
	case place == nil:
		return "", derive.InternalErrorf("a %s rule names no place", n.Type)
	case place.Zone != nil:
		z, err := derive.MustGet(f.zones, "FirewallZone", *place.Zone)
		if err != nil {
			return "", err
		}
		if len(z.links) == 0 {
			return "", derive.InternalErrorf("the zone %s holds no link", *place.Zone)
		}
		parts = append(parts, dir+" "+interfaceSet(z.links))
	case place.Interface != nil:
		parts = append(parts, dir+" "+interfaceSet([]string{*place.Interface}))
	default:
		return "", derive.InternalErrorf("a place names neither a zone nor an interface")
	}
	for _, side := range []struct {
		dir    string
		ranges []netip.Prefix
	}{{"saddr", n.Match.Source}, {"daddr", n.Match.Destination}} {
		if len(side.ranges) == 0 {
			continue
		}
		parts = append(parts, family+" "+side.dir+" "+set(prefixes(side.ranges)))
	}
	if n.Match.Protocol != nil {
		parts = append(parts, protocolMatch(*n.Match.Protocol, nil, n.Match.DestinationPorts, nil))
	}
	switch n.Type {
	case "masquerade":
		parts = append(parts, "masquerade")
	case "snat", "dnat":
		if n.To == nil {
			return "", derive.InternalErrorf("%s has no to", n.Type)
		}
		a := n.To.Address
		to := a.String()
		if n.To.Port != nil {
			if a.Is6() {
				to = "[" + to + "]"
			}
			to += ":" + strconv.Itoa(*n.To.Port)
		}
		parts = append(parts, fmt.Sprintf("%s %s to %s", n.Type, family, to))
	}
	return strings.Join(parts, " "), nil
}

// familyWords are nftables's words for the address families: ip and ip6.
var familyWords = map[derive.Family]string{derive.IPv4: "ip", derive.IPv6: "ip6"}

// ruleLines renders rule n of the policy: a line per family it matches, or one for both when it
// names neither addresses nor a family. A family its addresses or sets leave no address of has no
// line.
func (f *firewall) ruleLines(policy string, n int, r derive.FirewallRule) ([]string, error) {
	verdict := verdictOf(r.Action)
	if r.Log {
		// A log prefix holds 127 bytes at most.
		verdict = fmt.Sprintf("log prefix %q %s", fmt.Sprintf("%.100s %d: ", policy, n), verdict)
	}
	matches, err := f.matchLines(r)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(matches))
	for i, m := range matches {
		lines[i] = joinWords(m.match, verdict)
	}
	return lines, nil
}

// familyMatch is the match of a rule's terms in a family, "" for both.
type familyMatch struct {
	family derive.Family
	match  string
}

// matchLines are the matches of a rule's terms: one per family it matches, or one for both when
// it names neither addresses nor a family. A family its addresses or sets leave no address of
// has none. The rules of FirewallPolicy and PolicyRoute check that ports and ICMP types go with
// their protocols and that a rule matches a family.
func (f *firewall) matchLines(r derive.FirewallRule) ([]familyMatch, error) {
	protocol := ""
	if r.Protocol != nil {
		protocol = protocolMatch(*r.Protocol, r.SourcePorts, r.DestinationPorts, r.ICMPTypes)
	}
	families := []derive.Family{derive.IPv4, derive.IPv6}
	if r.Family != nil {
		families = []derive.Family{*r.Family}
	}
	switch {
	case r.Protocol != nil && *r.Protocol == "icmp":
		families = slices.DeleteFunc(families, func(f derive.Family) bool { return f != derive.IPv4 })
	case r.Protocol != nil && *r.Protocol == "icmpv6":
		families = slices.DeleteFunc(families, func(f derive.Family) bool { return f != derive.IPv6 })
	}
	if len(families) == 2 && r.Source == nil && r.Destination == nil {
		return []familyMatch{{"", protocol}}, nil
	}
	var matches []familyMatch
	for _, fam := range families {
		src, ok, err := f.addressMatch(fam, "saddr", r.Source)
		if err != nil || !ok {
			if err != nil {
				return nil, derive.Within("source", err)
			}
			continue
		}
		dst, ok, err := f.addressMatch(fam, "daddr", r.Destination)
		if err != nil || !ok {
			if err != nil {
				return nil, derive.Within("destination", err)
			}
			continue
		}
		match := joinWords(src, dst)
		// An ICMP type match says the family by itself.
		if match == "" && len(r.ICMPTypes) == 0 {
			match = "meta nfproto " + string(fam)
		}
		matches = append(matches, familyMatch{fam, joinWords(match, protocol)})
	}
	return matches, nil
}

// joinWords joins the parts that are not empty with spaces.
func joinWords(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// addressMatch is the match of the source or destination a on the side dir, saddr or daddr, in
// the family fam: of its AddressSet, or of its ranges of that family. ok is false when a has
// addresses but none of fam; a nil a matches every address. The rules check that a names a set or
// ranges, one of them.
func (f *firewall) addressMatch(fam derive.Family, dir string, a *derive.Addresses) (match string, ok bool, err error) {
	switch {
	case a == nil:
		return "", true, nil
	case a.Set != nil:
		s, err := derive.MustGet(f.sets, "AddressSet", *a.Set)
		if err != nil {
			return "", false, err
		}
		if s.Family != fam {
			return "", false, nil
		}
		return fmt.Sprintf("%s %s @addresses-%s", familyWords[fam], dir, *a.Set), true, nil
	}
	match, ok = rangeMatch(fam, dir, a.CIDRs)
	return match, ok, nil
}

// rangeMatch is the match of the address ranges of the family fam among ranges on the side dir,
// saddr or daddr. ok is false when ranges hold none of fam.
func rangeMatch(fam derive.Family, dir string, ranges []netip.Prefix) (match string, ok bool) {
	var mine []netip.Prefix
	for _, p := range ranges {
		if p.Addr().Is4() == (fam == derive.IPv4) {
			mine = append(mine, p)
		}
	}
	if len(mine) == 0 {
		return "", false
	}
	return familyWords[fam] + " " + dir + " " + set(prefixes(mine)), true
}

// prefixes are the address ranges l masked as nftables takes them. The rules of AddressSet and
// NATRule check that they are of the set's or rule's family.
func prefixes(l []netip.Prefix) []string {
	out := make([]string, len(l))
	for i, p := range l {
		out[i] = p.Masked().String()
	}
	return out
}

// protocolNumbers are the protocols nftables names by number, as it reads the others' names
// without /etc/protocols.
var protocolNumbers = map[derive.Protocol]string{"gre": "47", "esp": "50", "ah": "51"}

// protocolMatch matches the protocol with its ports or ICMP types. The rules of FirewallPolicy,
// PolicyRoute and NATRule check that ports go with tcp, udp or sctp, ICMP types with icmp or
// icmpv6 and packet-too-big with icmpv6, so each protocol reads what it takes alone.
func protocolMatch(protocol derive.Protocol, sourcePorts, destinationPorts []derive.PortRange, icmpTypes []derive.ICMPType) string {
	switch protocol {
	case "tcp", "udp", "sctp":
		var parts []string
		for _, side := range []struct {
			dir   string
			ports []derive.PortRange
		}{{"sport", sourcePorts}, {"dport", destinationPorts}} {
			if len(side.ports) > 0 {
				parts = append(parts, string(protocol)+" "+side.dir+" "+set(texts(side.ports)))
			}
		}
		if len(parts) == 0 {
			return "meta l4proto " + string(protocol)
		}
		return strings.Join(parts, " ")
	case "icmp", "icmpv6":
		if len(icmpTypes) == 0 {
			return "meta l4proto " + string(protocol)
		}
		return string(protocol) + " type " + set(texts(icmpTypes))
	}
	return "meta l4proto " + protocolNumbers[protocol]
}

// texts are the values of a string type as strings.
func texts[T ~string](l []T) []string {
	out := make([]string, len(l))
	for i, v := range l {
		out[i] = string(v)
	}
	return out
}

// verdictOf is nftables's statement of an action: reject answers with an administratively
// prohibited error of the packet's family.
func verdictOf(action derive.Verdict) string {
	if action == "reject" {
		return "reject with icmpx admin-prohibited"
	}
	return string(action)
}

// interfaceSet is a set of interface names and globs as nftables writes it.
func interfaceSet(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return set(quoted)
}

// set is the anonymous nftables set of the items.
func set(items []string) string {
	return "{ " + strings.Join(items, ", ") + " }"
}

// lines renders the opening: a line per family of its sources and destinations, or one without
// either. A family that one of them names and the other lacks has no line.
func (o opening) lines() []string {
	match := protocolMatch(o.protocol, nil, o.ports, nil)
	head := ""
	switch {
	case len(o.interfaces) > 0 && o.vrf != "":
		head = "iifname " + strconv.Quote(o.vrf) + " meta sdifname " + interfaceSet(o.interfaces) + " "
	case len(o.interfaces) > 0:
		head = "iifname " + interfaceSet(o.interfaces) + " "
	case o.vrf != "":
		head = "iifname " + strconv.Quote(o.vrf) + " "
	}
	tail := fmt.Sprintf("%s accept comment %q", match, o.comment)
	if len(o.sources) == 0 && len(o.destinations) == 0 {
		return []string{head + tail}
	}
	var lines []string
	for _, fam := range []derive.Family{derive.IPv4, derive.IPv6} {
		var parts []string
		for _, side := range []struct {
			dir    string
			ranges []netip.Prefix
		}{{"saddr", o.sources}, {"daddr", o.destinations}} {
			if len(side.ranges) == 0 {
				continue
			}
			match, ok := rangeMatch(fam, side.dir, side.ranges)
			if !ok {
				parts = nil
				break
			}
			parts = append(parts, match)
		}
		if len(parts) > 0 {
			lines = append(lines, head+strings.Join(parts, " ")+" "+tail)
		}
	}
	return lines
}

// linkLocal are the IPv6 link-local addresses, which DHCPv6 clients send from, and anyIPv4 every
// IPv4 address, which a DHCPv4 client without an address sends from as 0.0.0.0.
var (
	linkLocal = netip.MustParsePrefix("fe80::/10")
	anyIPv4   = netip.MustParsePrefix("0.0.0.0/0")
)

// dhcpOpenings are the DHCP ports of the DHCPv6 servers and of the relays on their links, in the
// links' VRF: DHCPv6's UDP port 547 from link-local addresses on an IPv6 DHCPSubnet's link and on
// a DHCPRelay's links with an IPv6 server, and DHCPv4's UDP port 67 on a DHCPRelay's links with
// an IPv4 server. Kea's DHCPv4 server reads and answers on raw sockets ahead of the firewall and
// needs none, and the servers' answers to the relay pass as replies to its requests.
func dhcpOpenings(d derive.Model) ([]opening, error) {
	subnets, err := d.Subnets()
	if err != nil {
		return nil, err
	}
	relays, err := d.Relays()
	if err != nil {
		return nil, err
	}
	var out []opening
	for _, s := range subnets {
		if !s.Is6() {
			continue
		}
		out = append(out, opening{comment: "DHCPSubnet " + s.Name, protocol: "udp", ports: []derive.PortRange{"547"},
			interfaces: s.Links, vrf: derive.VRFDevice(s.VRF), sources: []netip.Prefix{linkLocal}})
	}
	for _, r := range relays {
		var links []string
		for _, l := range r.Links {
			links = append(links, l.Link)
		}
		o := opening{comment: "DHCPRelay " + r.Name, protocol: "udp", interfaces: links, vrf: derive.VRFDevice(r.VRF)}
		if len(r.Servers[false]) > 0 {
			v4 := o
			v4.ports, v4.sources = []derive.PortRange{"67"}, []netip.Prefix{anyIPv4}
			out = append(out, v4)
		}
		if len(r.Servers[true]) > 0 {
			v6 := o
			v6.ports, v6.sources = []derive.PortRange{"547"}, []netip.Prefix{linkLocal}
			out = append(out, v6)
		}
	}
	return out, nil
}

// poolSet is the anonymous nftables set of the prefixes of a NAT64's explicit pool.
func poolSet(pools []netip.Prefix) string {
	l := make([]string, len(pools))
	for i, p := range pools {
		l[i] = p.String()
	}
	return set(l)
}
