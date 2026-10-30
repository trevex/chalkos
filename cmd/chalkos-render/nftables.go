package main

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
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
	d, err := readRouterDocuments(in)
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
	protocol   string
	ports      []portRange
	interfaces []string
	// vrf is the VRF of the service, "" for the default VRF. The input hook sees a packet from a
	// VRF's link with the VRF's device as iifname and the link as sdifname.
	vrf          string
	sources      []string
	destinations []string
}

// bgpOpenings are the ports of each BGPPeer, on its interfaces, of its VRF, or from its address
// alone: BGP's TCP port 179 and, with bfd, BFD's UDP ports 3784 and 3785 of single-hop sessions,
// and 4784 of multi-hop ones from an address peer; a peer on a link is one hop away. A peer of a
// VRF is opened in its VRF alone. The output frr refuses a peer on a VRF's device.
func bgpOpenings(peers []named[peerSpec]) ([]opening, error) {
	var out []opening
	for _, p := range peers {
		what := "BGPPeer " + p.Name
		o := opening{comment: what}
		s := p.Spec
		if !s.namesOneNeighbour() {
			return nil, fmt.Errorf("%s names not exactly one of address, interface and interfaces", what)
		}
		switch {
		case s.Address != nil:
			a, err := neighbourAddress(*s.Address)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", what, err)
			}
			o.sources = []string{netip.PrefixFrom(a, a.BitLen()).String()}
		case s.Interface != nil:
			o.interfaces = []string{*s.Interface}
		default:
			o.interfaces = s.Interfaces
		}
		if vrf := vrfOf(s.VRF); vrf != defaultVRF {
			if !plainInterface.MatchString(vrf) {
				return nil, fmt.Errorf("%s: the VRF %q is no VRF's name", what, vrf)
			}
			o.vrf = vrf
		}
		// An opening without interfaces and sources would accept the port from anywhere.
		if s.Address == nil {
			if err := checkInterfaces(what, o.interfaces); err != nil {
				return nil, err
			}
		}
		bgp := o
		bgp.protocol, bgp.ports = "tcp", []portRange{"179"}
		out = append(out, bgp)
		if s.BFD {
			bfd := o
			bfd.protocol, bfd.ports = "udp", []portRange{"3784", "3785"}
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
func resolverOpenings(resolvers []named[resolverSpec]) ([]opening, error) {
	var out []opening
	for _, r := range resolvers {
		what := "Resolver " + r.Name
		listen, allow, err := resolverAddresses(r.Spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		o := opening{comment: what, ports: []portRange{"53"}}
		// The input hook sees a packet from a VRF's link with the VRF's device as iifname.
		if vrf := vrfOf(r.Spec.VRF); vrf != defaultVRF {
			if !plainInterface.MatchString(vrf) {
				return nil, fmt.Errorf("%s: the VRF %q is no VRF's name", what, vrf)
			}
			o.vrf = vrf
		}
		for _, a := range listen {
			o.destinations = append(o.destinations, netip.PrefixFrom(a, a.BitLen()).String())
		}
		for _, p := range allow {
			o.sources = append(o.sources, p.String())
		}
		for _, proto := range []string{"udp", "tcp"} {
			x := o
			x.protocol = proto
			out = append(out, x)
		}
	}
	return out, nil
}

// firewall is what the ruleset is rendered from.
type firewall struct {
	router   routerSpec
	zones    map[string]zoneLinks
	sets     map[string]addressSetSpec
	setNames []string
	policies []named[policySpec]
	nat      []named[natSpec]
	openings []opening
	// ctZones are the VRFs with a conntrack zone and links, by name.
	ctZones []ctZone
	// nat64 are the NAT64 instances, whose links the forward chain and source NAT treat apart.
	nat64  []nat64Instance
	port   int
	closed bool
}

// ctZone is a VRF's conntrack zone and the links whose connections take it.
type ctZone struct {
	vrf   string
	zone  int
	links []string
}

func newFirewall(d routerDocuments, port int, closed bool) (*firewall, error) {
	f := &firewall{
		router:   d.router(),
		sets:     map[string]addressSetSpec{},
		policies: d.Policies,
		nat:      d.NAT,
		port:     port,
		closed:   closed,
	}
	devices := vrfDevices(d)
	if m := f.router.Management.Interfaces; m != nil {
		if err := checkInterfaces("Router: management.interfaces", m); err != nil {
			return nil, err
		}
		// The input hook sees the VRF's device for each of its links, while chalkd listens in
		// the default VRF alone.
		for _, i := range m {
			if dev, ok := vrfDeviceMatch(i, devices); ok {
				return nil, fmt.Errorf("Router: the management interface %s matches %s, the device of VRF %s, and chalkd answers in the default VRF alone; name the default VRF's links", i, dev, dev)
			}
		}
	}
	zones, err := zoneLinksOf(d, devices)
	if err != nil {
		return nil, err
	}
	f.zones = zones
	if f.nat64, err = nat64Instances(d); err != nil {
		return nil, err
	}
	zoneOf := map[int]string{}
	for _, v := range d.VRFs {
		if v.Spec.ConntrackZone == nil {
			continue
		}
		z := *v.Spec.ConntrackZone
		if z < 1 || z > 65535 {
			return nil, fmt.Errorf("VRF %s: conntrackZone %d is no zone from 1 to 65535", v.Name, z)
		}
		if other, ok := zoneOf[z]; ok {
			return nil, fmt.Errorf("VRF %s and VRF %s both use conntrack zone %d", other, v.Name, z)
		}
		zoneOf[z] = v.Name
		links, err := d.Membership.links(v.Name)
		if err != nil {
			return nil, err
		}
		// A NAT64's link carries the VRF's connections into and out of its namespace.
		for _, n := range f.nat64 {
			if n.vrf == v.Name {
				links = appendNew(links, nat64LinkPrefix+n.name)
			}
		}
		if len(links) > 0 {
			f.ctZones = append(f.ctZones, ctZone{vrf: v.Name, zone: z, links: links})
		}
	}
	for _, n := range d.NAT {
		for _, place := range []*natPlace{n.Spec.In, n.Spec.Out} {
			if place != nil && place.Interface != nil {
				if err := checkInterfaces("NATRule "+n.Name, []string{*place.Interface}); err != nil {
					return nil, err
				}
				// Prerouting and postrouting translate by the links, never by a VRF's device: an
				// interface that matches a device and no link translates nothing.
				if dev, ok := onlyDevice(d, devices, *place.Interface); ok {
					return nil, fmt.Errorf("NATRule %s: %s matches no link, only %s, the device of VRF %s; name the VRF's links or a zone of the VRF", n.Name, *place.Interface, dev, dev)
				}
			}
		}
	}
	for _, s := range d.Sets {
		if s.Spec.Family != "ipv4" && s.Spec.Family != "ipv6" {
			return nil, fmt.Errorf("AddressSet %s: family %q is neither ipv4 nor ipv6", s.Name, s.Spec.Family)
		}
		f.sets[s.Name] = s.Spec
		f.setNames = append(f.setNames, s.Name)
	}
	for _, p := range d.Policies {
		if p.Spec.From == "local" {
			return nil, fmt.Errorf("FirewallPolicy %s comes from local", p.Name)
		}
		for _, z := range []string{p.Spec.From, p.Spec.To} {
			if _, ok := f.zones[z]; !ok && z != "local" {
				return nil, fmt.Errorf("FirewallPolicy %s names the zone %s, which no FirewallZone declares", p.Name, z)
			}
		}
	}
	return f, nil
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
// interfaces are its in and its links, as before VRFs. It refuses a zone of no interface and no
// VRF, a VRF no document declares or that two zones hold, an interface that matches a VRF's
// device, which the input and forward hooks see for each of the VRF's links, and an interface
// that may be a link of a VRF another zone holds.
func zoneLinksOf(d routerDocuments, devices []string) (map[string]zoneLinks, error) {
	declared := map[string]bool{}
	for _, v := range d.VRFs {
		declared[v.Name] = true
	}
	held := map[string]string{}
	for _, z := range d.Zones {
		for _, v := range z.Spec.VRFs {
			if !declared[v] {
				return nil, fmt.Errorf("FirewallZone %s names the VRF %q, which no VRF document declares", z.Name, v)
			}
			if _, ok := held[v]; ok {
				return nil, fmt.Errorf("the VRF %s is in more than one FirewallZone, or twice in one", v)
			}
			held[v] = z.Name
		}
	}
	out := map[string]zoneLinks{}
	for _, z := range d.Zones {
		what := "FirewallZone " + z.Name
		if len(z.Spec.Interfaces) == 0 && len(z.Spec.VRFs) == 0 {
			return nil, fmt.Errorf("%s: a FirewallZone names an interface or a VRF; give it interfaces, vrfs or both", what)
		}
		if len(z.Spec.Interfaces) > 0 {
			if err := checkInterfaces(what, z.Spec.Interfaces); err != nil {
				return nil, err
			}
		}
		var zl zoneLinks
		for _, i := range z.Spec.Interfaces {
			if dev, ok := vrfDeviceMatch(i, devices); ok {
				return nil, fmt.Errorf("%s: %s matches %s, the device of VRF %s; name the VRF in vrfs", what, i, dev, dev)
			}
			vrfs, err := d.Membership.interfaceVRFs(i)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", what, err)
			}
			for _, v := range vrfs {
				if v == defaultVRF {
					zl.in = appendNew(zl.in, i)
					continue
				}
				if other, ok := held[v]; ok {
					if other != z.Name {
						return nil, fmt.Errorf("%s names %s, which may be a link of VRF %s, which FirewallZone %s holds; give each link one zone", what, i, v, other)
					}
					// The device of the zone's own VRF covers its links.
					continue
				}
				zl.sdif = appendNew(zl.sdif, i)
			}
			zl.links = appendNew(zl.links, i)
		}
		for _, v := range z.Spec.VRFs {
			links, err := d.Membership.links(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", what, err)
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

// vrfDevices are the VRFs' devices, sorted: those of the VRF documents and of the Networks' own
// VRF=.
func vrfDevices(d routerDocuments) []string {
	devices := map[string]bool{}
	for _, v := range d.VRFs {
		devices[v.Name] = true
	}
	for _, m := range d.Membership.Members {
		devices[m.vrf] = true
	}
	return slices.Sorted(maps.Keys(devices))
}

// onlyDevice returns the first VRF device of devices that the interface or glob i matches, when
// i matches no link: none the Network documents name and no glob of a VRF's links. A glob such
// as *, which matches links too, is not one.
func onlyDevice(d routerDocuments, devices []string, i string) (string, bool) {
	dev, ok := vrfDeviceMatch(i, devices)
	if !ok {
		return "", false
	}
	for _, l := range d.Links {
		if !slices.Contains(devices, l) && matchesGlob(i, l) {
			return "", false
		}
	}
	for _, m := range d.Membership.Members {
		if globsOverlap(i, m.glob) {
			return "", false
		}
	}
	return dev, true
}

// vrfDeviceMatch returns the first VRF device of devices, as vrfDevices finds them, that the
// interface or glob matches.
func vrfDeviceMatch(glob string, devices []string) (string, bool) {
	for _, dev := range devices {
		if matchesGlob(glob, dev) {
			return dev, true
		}
	}
	return "", false
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
// reverse-path filter, input, forward and the NAT chains.
func (f *firewall) ruleset() (string, error) {
	var b strings.Builder
	b.WriteString("# The router's firewall, rendered by chalkos-render. Loading the file replaces the table\n" +
		"# inet chalkos in one transaction.\n" +
		"table inet chalkos\n" +
		"delete table inet chalkos\n" +
		"table inet chalkos {\n")
	for _, name := range f.setNames {
		s := f.sets[name]
		elements, err := prefixes(s.Prefixes, s.Family)
		if err != nil {
			return "", fmt.Errorf("AddressSet %s: %w", name, err)
		}
		fmt.Fprintf(&b, "\tset addresses-%s {\n\t\ttype %s_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { %s }\n\t}\n",
			name, s.Family, strings.Join(elements, ", "))
	}
	for _, p := range f.policies {
		fmt.Fprintf(&b, "\tchain policy-%s {\n", p.Name)
		for i, r := range p.Spec.Rules {
			lines, err := f.ruleLines(p.Name, i+1, r)
			if err != nil {
				return "", fmt.Errorf("FirewallPolicy %s rule %d: %w", p.Name, i+1, err)
			}
			for _, l := range lines {
				b.WriteString("\t\t" + l + "\n")
			}
		}
		v, err := verdictOf(p.Spec.Default)
		if err != nil {
			return "", fmt.Errorf("FirewallPolicy %s: default: %w", p.Name, err)
		}
		b.WriteString("\t\t" + v + "\n\t}\n")
	}
	f.conntrackZones(&b)
	if err := f.prerouting(&b); err != nil {
		return "", err
	}
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
		fmt.Fprintf(b, "\tchain %s {\n\t\ttype filter hook %s priority raw; policy accept;\n", c.name, c.hook)
		for _, z := range f.ctZones {
			fmt.Fprintf(b, "\t\t%s %s ct zone set %d comment %q\n", c.dir, interfaceSet(z.links), z.zone, "VRF "+z.vrf)
		}
		b.WriteString("\t}\n")
	}
}

// prerouting is the reverse-path filter, which NixOS's firewall applies on every other image.
// The kernel's fib expression passes ICMPv6 from :: to link-scope addresses: the neighbour
// solicitations of duplicate address detection and the MLD reports of an address not yet assigned.
func (f *firewall) prerouting(b *strings.Builder) error {
	var fib string
	switch f.router.RPFilter {
	case "off":
		return nil
	case "loose":
		fib = "fib saddr . mark oif exists accept"
	case "strict":
		fib = "fib saddr . mark . iif oif exists accept"
	default:
		return fmt.Errorf("Router: rpFilter %q is none of off, loose and strict", f.router.RPFilter)
	}
	b.WriteString("\tchain prerouting {\n" +
		"\t\ttype filter hook prerouting priority mangle + 10; policy drop;\n" +
		"\t\tmeta nfproto ipv4 udp sport . udp dport { 67 . 68, 68 . 67 } accept\n" +
		"\t\t" + fib + "\n" +
		"\t}\n")
	return nil
}

// input accepts the protected traffic, chalkd's port on the management interfaces, the openings,
// then hands each connection from a zone to the policy to local of that zone, and drops the rest.
func (f *firewall) input(b *strings.Builder) error {
	b.WriteString("\tchain input {\n" +
		"\t\ttype filter hook input priority filter; policy drop;\n" +
		"\t\tiifname \"lo\" accept\n" +
		"\t\tct state established,related accept\n" +
		"\t\tct state invalid drop\n" +
		"\t\ticmpv6 type { " + icmpv6Local + " } accept\n" +
		"\t\ticmp type { " + icmpErrors + ", echo-request } accept\n" +
		"\t\tip6 daddr fe80::/64 udp sport 547 udp dport 546 accept\n")
	api := fmt.Sprintf("tcp dport %d accept", f.port)
	if f.router.Management.Interfaces != nil {
		api = "iifname " + interfaceSet(f.router.Management.Interfaces) + " " + api
	}
	b.WriteString("\t\t" + api + "\n")
	for _, o := range f.openings {
		lines, err := o.lines()
		if err != nil {
			return fmt.Errorf("the opening of %s: %w", o.comment, err)
		}
		for _, l := range lines {
			b.WriteString("\t\t" + l + "\n")
		}
	}
	for _, p := range f.policies {
		if p.Spec.To == "local" {
			for _, from := range f.zones[p.Spec.From].entries() {
				fmt.Fprintf(b, "\t\t%s jump policy-%s\n", from, p.Name)
			}
		}
	}
	b.WriteString("\t}\n")
	return nil
}

// forward accepts everything while no policy goes between two zones and the ruleset is not
// closed. Otherwise it accepts the protected traffic, the NAT64s' translated traffic into and out
// of their links, which no policy filters, and destination-translated connections, hands each connection to
// the policy of its pair of zones, and drops the rest, also from or to an interface in no zone. A
// policy to a zone of no link has no line.
func (f *firewall) forward(b *strings.Builder) {
	var between []named[policySpec]
	for _, p := range f.policies {
		if p.Spec.To != "local" {
			between = append(between, p)
		}
	}
	if len(between) == 0 && !f.closed {
		b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n\t}\n")
		return
	}
	b.WriteString("\tchain forward {\n" +
		"\t\ttype filter hook forward priority filter; policy drop;\n" +
		"\t\tct state established,related accept\n" +
		"\t\tct state invalid drop\n" +
		"\t\ticmpv6 type { " + icmpv6Errors + " } accept\n" +
		"\t\ticmp type { " + icmpErrors + " } accept\n")
	// A NAT64's link carries what Jool translated: out of it IPv6 from the prefix and IPv4 from
	// the pool or, for an own pool, the inner address; into it IPv6 to the prefix and IPv4 to an
	// explicit pool. An own pool's IPv4 comes back to the masqueraded connections alone. Anything
	// else on the link goes on to the zones, which hold no NAT64's link, and is dropped.
	for _, n := range f.nat64 {
		link, c := strconv.Quote(nat64LinkPrefix+n.name), strconv.Quote("NAT64 "+n.name)
		// The forward hook sees a packet from a VRF's link with the VRF's device as iifname.
		from := "iifname " + link
		if n.vrf != defaultVRF {
			from = "meta sdifname " + link
		}
		prefix := n.jool.Global.Pool6
		source := n.inner.String()
		if len(n.pools) > 0 {
			source = n.poolSet()
		}
		fmt.Fprintf(b, "\t\t%s ip6 saddr %s accept comment %s\n", from, prefix, c)
		fmt.Fprintf(b, "\t\t%s ip saddr %s accept comment %s\n", from, source, c)
		fmt.Fprintf(b, "\t\toifname %s ip6 daddr %s accept comment %s\n", link, prefix, c)
		if len(n.pools) > 0 {
			fmt.Fprintf(b, "\t\toifname %s ip daddr %s accept comment %s\n", link, n.poolSet(), c)
		}
	}
	if slices.ContainsFunc(f.nat, func(n named[natSpec]) bool { return n.Spec.Type == "dnat" }) {
		b.WriteString("\t\tct status dnat accept\n")
	}
	for _, p := range between {
		to := f.zones[p.Spec.To].links
		if len(to) == 0 {
			continue
		}
		for _, from := range f.zones[p.Spec.From].entries() {
			fmt.Fprintf(b, "\t\t%s oifname %s jump policy-%s\n", from, interfaceSet(to), p.Name)
		}
	}
	b.WriteString("\t}\n")
}

// translation renders the NATRule documents, in the order of their names: dnat in dstnat after the
// NAT64s' links and pools, masquerade and snat in srcnat after the NAT64s' lines. A ruleset without
// NAT rules and without an own pool of a NAT64 has neither chain.
func (f *firewall) translation(b *strings.Builder) error {
	var dst, src []string
	for _, n := range f.nat {
		line, err := f.natLine(n.Spec)
		if err != nil {
			return fmt.Errorf("NATRule %s: %w", n.Name, err)
		}
		if n.Spec.Type == "dnat" {
			dst = append(dst, line)
		} else {
			src = append(src, line)
		}
	}
	// No rule translates the destination of a connection out of a NAT64's link, which Jool
	// translated already, or to an explicit pool, whose addresses belong to the NAT64 whole.
	if len(dst) > 0 {
		var exempt []string
		for _, n := range f.nat64 {
			c := strconv.Quote("NAT64 " + n.name)
			exempt = append(exempt, fmt.Sprintf("iifname %s accept comment %s", strconv.Quote(nat64LinkPrefix+n.name), c))
			if len(n.pools) > 0 {
				exempt = append(exempt, fmt.Sprintf("ip daddr %s accept comment %s", n.poolSet(), c))
			}
		}
		dst = append(exempt, dst...)
	}
	if len(src) > 0 || slices.ContainsFunc(f.nat64, func(n nat64Instance) bool { return len(n.pools) == 0 }) {
		src = append(f.nat64Lines(), src...)
	}
	for _, c := range []struct {
		name, hook, priority string
		lines                []string
	}{{"dstnat", "prerouting", "dstnat", dst}, {"srcnat", "postrouting", "srcnat", src}} {
		if len(c.lines) == 0 {
			continue
		}
		fmt.Fprintf(b, "\tchain %s {\n\t\ttype nat hook %s priority %s; policy accept;\n", c.name, c.hook, c.priority)
		for _, l := range c.lines {
			b.WriteString("\t\t" + l + "\n")
		}
		b.WriteString("\t}\n")
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
		c := strconv.Quote("NAT64 " + n.name)
		lines = append(lines,
			fmt.Sprintf("oifname %s accept comment %s", strconv.Quote(nat64LinkPrefix+n.name), c),
			fmt.Sprintf("ip6 saddr %s accept comment %s", n.jool.Global.Pool6, c))
		if len(n.pools) == 0 {
			lines = append(lines,
				fmt.Sprintf("ip saddr %s meta l4proto { tcp, udp } masquerade to :%s comment %s", n.inner, ownPorts, c),
				fmt.Sprintf("ip saddr %s masquerade comment %s", n.inner, c))
			continue
		}
		lines = append(lines, fmt.Sprintf("ip saddr %s accept comment %s", n.poolSet(), c))
	}
	return lines
}

func (f *firewall) natLine(n natSpec) (string, error) {
	family, ok := familyWords[n.Family]
	if !ok {
		return "", fmt.Errorf("family %q is neither ipv4 nor ipv6", n.Family)
	}
	parts := []string{"meta nfproto " + n.Family}
	place, dir := n.Out, "oifname"
	if n.Type == "dnat" {
		place, dir = n.In, "iifname"
	}
	if place == nil {
		return "", fmt.Errorf("a %s rule names no place", n.Type)
	}
	switch {
	case place.Zone != nil && place.Interface == nil:
		z, ok := f.zones[*place.Zone]
		if !ok {
			return "", fmt.Errorf("the zone %s is no FirewallZone", *place.Zone)
		}
		if len(z.links) == 0 {
			return "", fmt.Errorf("the zone %s holds no link", *place.Zone)
		}
		parts = append(parts, dir+" "+interfaceSet(z.links))
	case place.Interface != nil && place.Zone == nil:
		parts = append(parts, dir+" "+interfaceSet([]string{*place.Interface}))
	default:
		return "", fmt.Errorf("a place names a zone or an interface, one of them")
	}
	for _, side := range []struct {
		dir    string
		ranges []string
	}{{"saddr", n.Match.Source}, {"daddr", n.Match.Destination}} {
		if len(side.ranges) == 0 {
			continue
		}
		elements, err := prefixes(side.ranges, n.Family)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s %s { %s }", family, side.dir, strings.Join(elements, ", ")))
	}
	if n.Match.Protocol != nil {
		proto, err := protocolMatch(*n.Match.Protocol, nil, n.Match.DestinationPorts, nil)
		if err != nil {
			return "", err
		}
		parts = append(parts, proto)
	} else if len(n.Match.DestinationPorts) > 0 {
		return "", fmt.Errorf("ports need match.protocol")
	}
	switch n.Type {
	case "masquerade":
		if n.To != nil {
			return "", fmt.Errorf("masquerade translates to the interface's address and has no to")
		}
		parts = append(parts, "masquerade")
	case "snat", "dnat":
		if n.To == nil {
			return "", fmt.Errorf("%s needs to", n.Type)
		}
		a, err := netip.ParseAddr(n.To.Address)
		if err != nil || (a.Is4() != (n.Family == "ipv4")) {
			return "", fmt.Errorf("to.address %q is no %s address", n.To.Address, n.Family)
		}
		to := a.String()
		if n.To.Port != nil {
			if n.Match.Protocol == nil {
				return "", fmt.Errorf("to.port needs match.protocol")
			}
			if *n.To.Port < 1 || *n.To.Port > 65535 {
				return "", fmt.Errorf("to.port %d is no port from 1 to 65535", *n.To.Port)
			}
			if a.Is6() {
				to = "[" + to + "]"
			}
			to += ":" + strconv.Itoa(*n.To.Port)
		}
		parts = append(parts, fmt.Sprintf("%s %s to %s", n.Type, family, to))
	default:
		return "", fmt.Errorf("type %q is none of masquerade, snat and dnat", n.Type)
	}
	return strings.Join(parts, " "), nil
}

// familyWords are nftables's words for the address families: ip and ip6.
var familyWords = map[string]string{"ipv4": "ip", "ipv6": "ip6"}

// ruleLines renders rule n of the policy: a line per family it matches, or one for both when it
// names neither addresses nor a family. A family its addresses or sets leave no address of has no
// line.
func (f *firewall) ruleLines(policy string, n int, r firewallRule) ([]string, error) {
	verdict, err := verdictOf(r.Action)
	if err != nil {
		return nil, fmt.Errorf("action: %w", err)
	}
	if r.Log {
		// A log prefix holds 127 bytes at most.
		verdict = fmt.Sprintf("log prefix %q %s", fmt.Sprintf("%.100s %d: ", policy, n), verdict)
	}
	protocol := ""
	if r.Protocol != nil {
		if protocol, err = protocolMatch(*r.Protocol, r.SourcePorts, r.DestinationPorts, r.ICMPTypes); err != nil {
			return nil, err
		}
	} else if len(r.SourcePorts) > 0 || len(r.DestinationPorts) > 0 || len(r.ICMPTypes) > 0 {
		return nil, fmt.Errorf("ports and ICMP types need a protocol")
	}
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, " ")
	}
	families := []string{"ipv4", "ipv6"}
	if r.Family != nil {
		if _, ok := familyWords[*r.Family]; !ok {
			return nil, fmt.Errorf("family %q is neither ipv4 nor ipv6", *r.Family)
		}
		families = []string{*r.Family}
	}
	switch {
	case r.Protocol != nil && *r.Protocol == "icmp":
		families = slices.DeleteFunc(families, func(f string) bool { return f != "ipv4" })
	case r.Protocol != nil && *r.Protocol == "icmpv6":
		families = slices.DeleteFunc(families, func(f string) bool { return f != "ipv6" })
	}
	if len(families) == 2 && len(r.Source) == 0 && len(r.Destination) == 0 {
		return []string{join(protocol, verdict)}, nil
	}
	var lines []string
	for _, fam := range families {
		src, ok, err := f.addressMatch(fam, "saddr", r.Source)
		if err != nil || !ok {
			if err != nil {
				return nil, fmt.Errorf("source: %w", err)
			}
			continue
		}
		dst, ok, err := f.addressMatch(fam, "daddr", r.Destination)
		if err != nil || !ok {
			if err != nil {
				return nil, fmt.Errorf("destination: %w", err)
			}
			continue
		}
		match := join(src, dst)
		// An ICMP type match says the family by itself.
		if match == "" && len(r.ICMPTypes) == 0 {
			match = "meta nfproto " + fam
		}
		lines = append(lines, join(match, protocol, verdict))
	}
	return lines, nil
}

// addressMatch is the match of the address list l on the side dir, saddr or daddr, in the family
// fam: of its AddressSet, or of its ranges of that family. ok is false when the list has
// addresses but none of fam; an empty list matches every address.
func (f *firewall) addressMatch(fam, dir string, l []string) (match string, ok bool, err error) {
	if len(l) == 0 {
		return "", true, nil
	}
	if name, isSet := strings.CutPrefix(l[0], "set:"); isSet {
		if len(l) > 1 {
			return "", false, fmt.Errorf("set:%s stands alone", name)
		}
		s, found := f.sets[name]
		if !found {
			return "", false, fmt.Errorf("the AddressSet %s does not exist", name)
		}
		if s.Family != fam {
			return "", false, nil
		}
		return fmt.Sprintf("%s %s @addresses-%s", familyWords[fam], dir, name), true, nil
	}
	var mine []string
	for _, a := range l {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			return "", false, fmt.Errorf("%q is no address range", a)
		}
		if p.Addr().Is4() == (fam == "ipv4") {
			mine = append(mine, p.Masked().String())
		}
	}
	if len(mine) == 0 {
		return "", false, nil
	}
	return fmt.Sprintf("%s %s { %s }", familyWords[fam], dir, strings.Join(mine, ", ")), true, nil
}

// prefixes are the address ranges l, all of the family fam, masked as nftables takes them.
func prefixes(l []string, fam string) ([]string, error) {
	out := make([]string, 0, len(l))
	for _, a := range l {
		p, err := netip.ParsePrefix(a)
		if err != nil || p.Addr().Is4() != (fam == "ipv4") {
			return nil, fmt.Errorf("%q is no %s address range", a, fam)
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}

// protocolNumbers are the protocols nftables names by number, as it reads the others' names
// without /etc/protocols.
var protocolNumbers = map[string]string{"gre": "47", "esp": "50", "ah": "51"}

// icmpTypeNames are the ICMP and ICMPv6 types a rule may match, which nftables names alike for
// both protocols; packet-too-big is ICMPv6's alone.
var icmpTypeNames = []string{"echo-request", "echo-reply", "destination-unreachable", "time-exceeded", "parameter-problem", "packet-too-big"}

// protocolMatch matches the protocol with its ports or ICMP types.
func protocolMatch(protocol string, sourcePorts, destinationPorts []portRange, icmpTypes []string) (string, error) {
	switch protocol {
	case "tcp", "udp", "sctp":
		if len(icmpTypes) > 0 {
			return "", fmt.Errorf("ICMP types need the protocol icmp or icmpv6")
		}
		var parts []string
		for _, side := range []struct {
			dir   string
			ports []portRange
		}{{"sport", sourcePorts}, {"dport", destinationPorts}} {
			if len(side.ports) > 0 {
				parts = append(parts, fmt.Sprintf("%s %s { %s }", protocol, side.dir, joinPorts(side.ports)))
			}
		}
		if len(parts) == 0 {
			return "meta l4proto " + protocol, nil
		}
		return strings.Join(parts, " "), nil
	case "icmp", "icmpv6":
		if len(sourcePorts) > 0 || len(destinationPorts) > 0 {
			return "", fmt.Errorf("ports need the protocol tcp, udp or sctp")
		}
		for _, t := range icmpTypes {
			if !slices.Contains(icmpTypeNames, t) {
				return "", fmt.Errorf("%q is no ICMP type the firewall matches", t)
			}
		}
		if protocol == "icmp" && slices.Contains(icmpTypes, "packet-too-big") {
			return "", fmt.Errorf("packet-too-big is an ICMPv6 type")
		}
		if len(icmpTypes) == 0 {
			return "meta l4proto " + protocol, nil
		}
		return fmt.Sprintf("%s type { %s }", protocol, strings.Join(icmpTypes, ", ")), nil
	}
	number, ok := protocolNumbers[protocol]
	if !ok {
		return "", fmt.Errorf("protocol %q is not one the firewall matches", protocol)
	}
	if len(sourcePorts) > 0 || len(destinationPorts) > 0 || len(icmpTypes) > 0 {
		return "", fmt.Errorf("the protocol %s has no ports or ICMP types", protocol)
	}
	return "meta l4proto " + number, nil
}

func joinPorts(ports []portRange) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}

// verdictOf is nftables's statement of an action: reject answers with an administratively
// prohibited error of the packet's family.
func verdictOf(action string) (string, error) {
	switch action {
	case "accept", "drop":
		return action, nil
	case "reject":
		return "reject with icmpx admin-prohibited", nil
	}
	return "", fmt.Errorf("%q is none of accept, drop and reject", action)
}

// interfaceSet is a set of interface names and globs as nftables writes it.
func interfaceSet(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return "{ " + strings.Join(quoted, ", ") + " }"
}

// lines renders the opening: a line per family of its sources and destinations, or one without
// either. A family that one of them names and the other lacks has no line.
func (o opening) lines() ([]string, error) {
	match, err := protocolMatch(o.protocol, nil, o.ports, nil)
	if err != nil {
		return nil, err
	}
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
		return []string{head + tail}, nil
	}
	var lines []string
	for _, fam := range []string{"ipv4", "ipv6"} {
		var parts []string
		for _, side := range []struct {
			dir    string
			ranges []string
		}{{"saddr", o.sources}, {"daddr", o.destinations}} {
			if len(side.ranges) == 0 {
				continue
			}
			var mine []string
			for _, a := range side.ranges {
				p, err := netip.ParsePrefix(a)
				if err != nil {
					return nil, fmt.Errorf("%q is no address range", a)
				}
				if p.Addr().Is4() == (fam == "ipv4") {
					mine = append(mine, p.Masked().String())
				}
			}
			if len(mine) == 0 {
				parts = nil
				break
			}
			parts = append(parts, fmt.Sprintf("%s %s { %s }", familyWords[fam], side.dir, strings.Join(mine, ", ")))
		}
		if len(parts) > 0 {
			lines = append(lines, head+strings.Join(parts, " ")+" "+tail)
		}
	}
	return lines, nil
}
