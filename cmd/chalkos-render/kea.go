package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The output kea: the config of Kea's DHCPv4 and DHCPv6 servers of each VRF whose links
// DHCPSubnet documents serve, on the links the subnets name. The kind's rules check each subnet
// on its own: its range, its pools and reservations against the range, each other and the
// gateway, DNS and boot servers, and its options. The renderer checks what spans documents: one
// VRF per subnet, one subnet of each family per link, no overlap and no number twice within a
// server, a static address of the router in the subnet on each link, kept out of the pools and
// reservations, what a subnet delegates against the router's addresses and routes, and router
// advertisements that send IPv6 clients to the DHCPv6 server. The output's check runs Kea's own,
// kea-dhcp4 -t and kea-dhcp6 -t, on each config, which refuses pools that run backwards, overlap
// or leave the subnet, a reservation outside the subnet and a lease past 4294967295 seconds; the
// renderer guards what Kea's check passes and would panic, or what Kea would read as more values.

// keaServer is one of Kea's servers: that of a VRF and a family.
type keaServer struct {
	vrf string
	v6  bool
}

// family is dhcp4 or dhcp6, the server's part of its program's, config's, socket's and lease
// file's names.
func (s keaServer) family() string {
	if s.v6 {
		return "dhcp6"
	}
	return "dhcp4"
}

// file is the server's config file: kea-dhcp4.json for the default VRF's DHCPv4 server,
// kea-dhcp6-<vrf>.json for a VRF's DHCPv6 server.
func (s keaServer) file() string {
	return "kea-" + s.instance() + ".json"
}

// instance is the part of the server's control socket's and lease file's names: dhcp4 for the
// default VRF's DHCPv4 server, dhcp6-<vrf> for a VRF's DHCPv6 server. Kea takes sockets in
// /run/kea alone and lease files in /var/lib/kea alone, so the servers share both directories.
func (s keaServer) instance() string {
	if s.vrf == defaultVRF {
		return s.family()
	}
	return s.family() + "-" + s.vrf
}

// keaRetryWait and keaRetries are how often Kea tries again to open the sockets of a link that is
// down or has no address yet, as at boot: every 5 seconds for as long as it runs.
const (
	keaRetryWait = 5000
	keaRetries   = math.MaxUint32
)

// dhcpName is a DNS name as the schema takes a domain: no comma, which Kea's option data would
// read as the start of another value.
var dhcpName = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)*[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// leaseTime is a lease's duration as the schema takes it.
var leaseTime = regexp.MustCompile(`^([1-9][0-9]{0,5})([smhd])$`)

// servedSubnet is a DHCPSubnet with the links it is served on and the addresses it was checked to.
type servedSubnet struct {
	name  string
	spec  dhcpSubnetSpec
	links []string
	vrf   string
	// prefix is the subnet, masked, and id Kea's number for it, unique within its server: an IPv4
	// subnet's first address as an integer, as no two subnets of a VRF overlap, and a hash of an
	// IPv6 subnet; a subnet keeps its number, and its leases, while documents come and go or are
	// renamed.
	prefix netip.Prefix
	id     uint32
	pools  [][2]netip.Addr
	// seconds is the lease time.
	seconds uint64
	// delegated is the range an IPv6 subnet delegates prefixes of length length from, invalid
	// for one that delegates none.
	delegated netip.Prefix
	length    int
}

// server is the Kea server that serves the subnet.
func (s servedSubnet) server() keaServer {
	return keaServer{vrf: s.vrf, v6: subnetIs6(s.prefix)}
}

// renderKea writes the config of Kea's DHCPv4 and DHCPv6 servers of each VRF whose links
// DHCPSubnet documents serve: out/kea-dhcp4.json and out/kea-dhcp6.json for the default VRF,
// out/kea-dhcp4-<vrf>.json and out/kea-dhcp6-<vrf>.json for a VRF. A node without a DHCPSubnet
// gets no file and runs no server. pdHook is the program Kea's run_script hook runs for an IPv6
// server that delegates prefixes.
func renderKea(pdHook string, in map[string]any, out string) error {
	if !strings.HasPrefix(pdHook, "/") {
		return fmt.Errorf("%q is no absolute path of the program that records delegated prefixes", pdHook)
	}
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
	// served are the subnets of each link, by family.
	served := map[keaServer]map[string]string{}
	byServer := map[keaServer][]servedSubnet{}
	for _, s := range d.DHCPSubnets {
		sub, err := serveSubnet(d, units, devices, s)
		if err != nil {
			return within("DHCPSubnet "+s.Name, err)
		}
		family := keaServer{v6: subnetIs6(sub.prefix)}
		if served[family] == nil {
			served[family] = map[string]string{}
		}
		for _, l := range sub.links {
			if other, ok := served[family][l]; ok {
				return fmt.Errorf("DHCPSubnet %s and DHCPSubnet %s both serve %s; a link takes one subnet of each family", other, s.Name, l)
			}
			served[family][l] = s.Name
		}
		for _, other := range byServer[sub.server()] {
			if err := apart(other, sub); err != nil {
				return err
			}
		}
		byServer[sub.server()] = append(byServer[sub.server()], sub)
	}
	if err := delegableWithin(byServer); err != nil {
		return err
	}
	servers := slices.Collect(maps.Keys(byServer))
	slices.SortFunc(servers, func(a, b keaServer) int {
		return strings.Compare(vrfOrder(a.vrf)+" "+a.family(), vrfOrder(b.vrf)+" "+b.family())
	})
	for _, srv := range servers {
		var config map[string]any
		if srv.v6 {
			config = kea6Config(srv, byServer[srv], pdHook)
		} else {
			config = keaConfig(srv, byServer[srv])
		}
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(out, srv.file(), append(data, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// apart refuses two subnets of one server whose ranges overlap, the ranges they delegate
// prefixes from included, or whose numbers are the same.
func apart(a, b servedSubnet) error {
	ranges := func(s servedSubnet) []netip.Prefix {
		if s.delegated.IsValid() {
			return []netip.Prefix{s.prefix, s.delegated}
		}
		return []netip.Prefix{s.prefix}
	}
	for _, p := range ranges(a) {
		for _, q := range ranges(b) {
			if p.Overlaps(q) {
				return fmt.Errorf("DHCPSubnet %s and DHCPSubnet %s overlap in VRF %s: %s and %s", a.name, b.name, a.vrf, p, q)
			}
		}
	}
	if a.id == b.id {
		return fmt.Errorf("DHCPSubnet %s and DHCPSubnet %s get the same number %d in Kea; change one subnet's range", a.name, b.name, a.id)
	}
	return nil
}

// subnetIs6 tells whether a DHCPSubnet of the range p is served by Kea's DHCPv6 server. An
// IPv4-mapped range, which the rules refuse, is IPv6 to Kea, so the relays and the firewall take
// it as IPv6 too.
func subnetIs6(p netip.Prefix) bool { return p.Addr().Is6() }

// serveSubnet finds the links of the subnet s, their VRF and the router's addresses in it, which
// no pool or reservation holds. The rules have checked the rest of the subnet's addresses.
func serveSubnet(d routerDocuments, units []networkUnit, devices []string, s named[dhcpSubnetSpec]) (servedSubnet, error) {
	sub := servedSubnet{name: s.Name, spec: s.Spec}
	p, err := netip.ParsePrefix(s.Spec.Subnet)
	if err != nil {
		return sub, internalErrorf("the subnet %q is no address range", s.Spec.Subnet)
	}
	if err := keaValues(s.Spec); err != nil {
		return sub, err
	}
	if subnetIs6(p) {
		return serveSubnet6(d, units, devices, s, p)
	}
	sub.prefix = p.Masked()
	// Kea's check refuses the number 0, of a subnet at 0.0.0.0, which the rules refuse first.
	sub.id = binary.BigEndian.Uint32(sub.prefix.Addr().AsSlice())
	if sub.links, sub.vrf, err = subnetLinks(d, devices, s.Spec.Interfaces, subnetWords); err != nil {
		return sub, err
	}
	own, err := ownAddresses(d, units, sub, "which Kea answers its clients from")
	if err != nil {
		return sub, err
	}
	if sub.pools, err = ownApart(s.Spec, own, "the router's own address on the subnet's links"); err != nil {
		return sub, err
	}
	sub.seconds, err = leaseSeconds(s.Spec.LeaseTime)
	return sub, err
}

// keaValues guards, with an internal error, the values of the subnet s that Kea's config takes as
// they are and Kea's check passes: an address that does not parse, which netip.MustParseAddr
// panics on, or that has a zone, which netip takes of any characters and Kea's option data splits
// at a comma into more values, and a domain that is no DNS name, which Kea's option data splits
// as well. The schema refuses each of them.
func keaValues(s dhcpSubnetSpec) error {
	var addresses []string
	if s.Gateway != nil {
		addresses = append(addresses, *s.Gateway)
	}
	addresses = append(addresses, s.DNS...)
	for _, p := range s.Pools {
		addresses = append(addresses, p.First, p.Last)
	}
	for _, r := range s.Reservations {
		addresses = append(addresses, r.Address)
	}
	if s.Boot != nil && s.Boot.Server != nil {
		addresses = append(addresses, *s.Boot.Server)
	}
	for _, a := range addresses {
		if addr, err := netip.ParseAddr(a); err != nil || addr.Zone() != "" {
			return internalErrorf("%q is no address without a zone", a)
		}
	}
	if dom := s.Domain; dom != nil && !dhcpName.MatchString(*dom) {
		return internalErrorf("%q is no domain", *dom)
	}
	return nil
}

// ownApart are the pools of the subnet s, each its first and last address. It refuses a pool or
// reservation that holds one of the router's own addresses own on the subnet's links, which Kea
// would lease to a client; what says what they are. The rules have kept the pools and
// reservations inside the subnet and apart from each other and from the gateway and the DNS and
// boot servers, and keaValues has parsed their addresses.
func ownApart(s dhcpSubnetSpec, own []netip.Addr, what string) ([][2]netip.Addr, error) {
	var pools [][2]netip.Addr
	for _, p := range s.Pools {
		first, last := netip.MustParseAddr(p.First), netip.MustParseAddr(p.Last)
		for _, o := range own {
			if !o.Less(first) && !last.Less(o) {
				return nil, fmt.Errorf("the pool %s-%s holds %s, %s", first, last, o, what)
			}
		}
		pools = append(pools, [2]netip.Addr{first, last})
	}
	for _, r := range s.Reservations {
		if a := netip.MustParseAddr(r.Address); slices.Contains(own, a) {
			return nil, fmt.Errorf("the reservation of %s, %s, is %s", r.MAC, a, what)
		}
	}
	return pools, nil
}

// subnetWords ends the refusal of a subnet's links in two VRFs.
var subnetWords = "a subnet is served in one VRF"

// subnetLinks are the links the interfaces of a subnet or relay serve it on, sorted, and their
// VRF; oneVRF ends the refusal of links of two VRFs. Kea reads and answers DHCPv4 on raw sockets
// ahead of the firewall, so a document names each of its links, as the schema's pattern, which
// takes no glob, holds it to. It refuses an interface that is a VRF's device or a NAT64's link,
// links of two VRFs and links in a VRF of a Network's VRF= that no VRF document declares. It
// guards, with an internal error, no interface, as an IPv6 subnet's config names its first, and
// a name that is no interface's, which dnsmasq's config and the firewall's ruleset would read as
// syntax.
func subnetLinks(d routerDocuments, devices []string, interfaces []string, oneVRF string) ([]string, string, error) {
	if len(interfaces) == 0 {
		return nil, "", internalErrorf("interfaces names no interface")
	}
	var links []string
	for _, i := range interfaces {
		if !plainInterface.MatchString(i) {
			return nil, "", internalErrorf("interfaces: %q is no interface name", i)
		}
		if slices.Contains(devices, i) {
			return nil, "", fmt.Errorf("%s is the device of VRF %s; name the VRF's links", i, i)
		}
		for _, n := range d.NAT64 {
			if i == nat64LinkPrefix+n.Name {
				return nil, "", fmt.Errorf("%s is the link of NAT64 %s", i, n.Name)
			}
		}
		links = appendNew(links, i)
	}
	slices.Sort(links)
	vrf := ""
	for _, l := range links {
		v, err := d.Membership.linkVRF(l)
		if err != nil {
			return nil, "", err
		}
		if vrf != "" && v != vrf {
			return nil, "", fmt.Errorf("its links are in VRF %s and VRF %s; %s", vrf, v, oneVRF)
		}
		vrf = v
	}
	if vrf != defaultVRF && !slices.ContainsFunc(d.VRFs, func(v named[vrfSpec]) bool { return v.Name == vrf }) {
		return nil, "", fmt.Errorf("its links are in the VRF %q, which no VRF document declares", vrf)
	}
	return links, vrf, nil
}

// linkNetwork is the name of the Network document networkd picks for the link, which the
// renderer reads the link's addresses and router advertisements from: one that matches it by
// Name= alone.
func linkNetwork(units []networkUnit, link, why string) (string, error) {
	cs := candidates(units, link)
	if len(cs) == 0 || cs[0].narrowed {
		return "", fmt.Errorf("no Network document matches %s by Name= alone, so the renderer cannot find %s; give the link one", link, why)
	}
	return cs[0].name, nil
}

// ownAddresses are the router's static addresses in the subnet on its links, which the Network
// networkd picks for each link gives. Kea answers a DHCPv4 client on a link from the link's
// address in the subnet, and the address routes an IPv6 subnet's addresses to the link, so it
// refuses a link without one; why ends the refusal.
func ownAddresses(d routerDocuments, units []networkUnit, sub servedSubnet, why string) ([]netip.Addr, error) {
	var own []netip.Addr
	for _, l := range sub.links {
		network, err := linkNetwork(units, l, "its address in the subnet "+sub.prefix.String())
		if err != nil {
			return nil, err
		}
		found := false
		for _, a := range d.Addresses {
			if a.network == network && sub.prefix.Contains(a.prefix.Addr()) {
				own, found = appendNewAddr(own, a.prefix.Addr()), true
			}
		}
		if !found {
			return nil, fmt.Errorf("Network %s gives %s no static address in the subnet %s, %s", network, l, sub.prefix, why)
		}
	}
	return own, nil
}

// appendNewAddr appends a to l unless l holds it.
func appendNewAddr(l []netip.Addr, a netip.Addr) []netip.Addr {
	if slices.Contains(l, a) {
		return l
	}
	return append(l, a)
}

// leaseSeconds is a lease time of the schema in seconds. It guards, with an internal error, a
// time the schema refuses, which it reads no seconds of. Kea's check refuses more than 4294967295
// seconds, which the rules refuse first.
func leaseSeconds(t string) (uint64, error) {
	m := leaseTime.FindStringSubmatch(t)
	if m == nil {
		return 0, internalErrorf("leaseTime %q is no number and s, m, h or d", t)
	}
	n, _ := strconv.ParseUint(m[1], 10, 64)
	return n * map[string]uint64{"s": 1, "m": 60, "h": 3600, "d": 86400}[m[2]], nil
}

// keaConfig is the config of the DHCPv4 server srv: raw sockets on the subnets' links, its
// control socket with the lease commands, its leases in a file, a client class of each subnet's
// boot file and the subnets.
func keaConfig(srv keaServer, subnets []servedSubnet) map[string]any {
	instance := srv.instance()
	var links []string
	var classes, subnet4 []any
	for _, s := range subnets {
		for _, l := range s.links {
			links = appendNew(links, l)
		}
		classes = append(classes, bootClasses(s)...)
		subnet4 = append(subnet4, keaSubnet(s))
	}
	slices.Sort(links)
	dhcp4 := map[string]any{
		// Raw sockets take the requests of clients without an address, and send the replies
		// through the link, ahead of the firewall.
		"interfaces-config": map[string]any{
			"interfaces":                      links,
			"dhcp-socket-type":                "raw",
			"service-sockets-retry-wait-time": keaRetryWait,
			"service-sockets-max-retries":     keaRetries,
		},
		"control-sockets": []any{map[string]any{"socket-type": "unix", "socket-name": "/run/kea/" + instance + ".sock"}},
		"lease-database":  map[string]any{"type": "memfile", "persist": true, "name": "/var/lib/kea/" + instance + ".leases", "lfc-interval": 3600},
		"hooks-libraries": []any{map[string]any{"library": "libdhcp_lease_cmds.so"}},
		"subnet4":         subnet4,
		"loggers":         keaLoggers(srv),
	}
	if len(classes) > 0 {
		dhcp4["client-classes"] = classes
	}
	return map[string]any{"Dhcp4": dhcp4}
}

// keaLoggers are the loggers of the server srv: Kea's messages go to the journal, which stamps
// them. Each command on the control socket logs a warning of the dhcp logger, and the commands
// logger names each, which chalkd's questions would fill the journal with.
func keaLoggers(srv keaServer) []any {
	name := "kea-" + srv.family()
	return []any{
		keaLogger(name, "INFO"),
		keaLogger(name+".dhcp", "ERROR"),
		keaLogger(name+".commands", "WARN"),
	}
}

// keaLogger logs the messages of the logger name from severity up to the journal.
func keaLogger(name, severity string) map[string]any {
	return map[string]any{
		"name":           name,
		"severity":       severity,
		"output-options": []any{map[string]any{"output": "stdout", "pattern": "%-5p [%c] %m\n"}},
	}
}

// keaSubnet is the subnet s as Kea's subnet4 takes it.
func keaSubnet(s servedSubnet) map[string]any {
	var pools []any
	for _, p := range s.pools {
		pools = append(pools, map[string]any{"pool": p[0].String() + " - " + p[1].String()})
	}
	var options []any
	option := func(name, data string) {
		options = append(options, map[string]any{"name": name, "data": data})
	}
	if s.spec.Gateway != nil {
		option("routers", netip.MustParseAddr(*s.spec.Gateway).String())
	}
	if len(s.spec.DNS) > 0 {
		var dns []string
		for _, a := range s.spec.DNS {
			dns = append(dns, netip.MustParseAddr(a).String())
		}
		option("domain-name-servers", strings.Join(dns, ", "))
	}
	if s.spec.Domain != nil {
		option("domain-name", *s.spec.Domain)
		option("domain-search", *s.spec.Domain)
	}
	var reservations []any
	for _, r := range s.spec.Reservations {
		h := map[string]any{"hw-address": r.MAC, "ip-address": netip.MustParseAddr(r.Address).String()}
		if r.Hostname != nil {
			h["hostname"] = *r.Hostname
		}
		reservations = append(reservations, h)
	}
	subnet := map[string]any{
		"id":             s.id,
		"subnet":         s.prefix.String(),
		"valid-lifetime": s.seconds,
		// chalkd and the diagnostic dhcp-leases name a subnet by its document.
		"user-context": map[string]any{"name": s.name},
	}
	if pools != nil {
		subnet["pools"] = pools
	}
	if options != nil {
		subnet["option-data"] = options
	}
	if reservations != nil {
		subnet["reservations"] = reservations
	}
	if names := bootClassNames(s); names != nil {
		subnet["evaluate-additional-classes"] = names
	}
	return subnet
}

// bootClassNames are the names of the client classes of the subnet's boot files, which Kea
// evaluates once it chose the subnet: <subnet>-ipxe, -uefi and -bios.
func bootClassNames(s servedSubnet) []string {
	b := s.spec.Boot
	if b == nil {
		return nil
	}
	var names []string
	for _, c := range []struct {
		file *string
		name string
	}{{b.IPXE, "ipxe"}, {b.UEFI, "uefi"}, {b.BIOS, "bios"}} {
		if c.file != nil {
			names = append(names, s.name+"-"+c.name)
		}
	}
	return names
}

// bootClasses are the client classes of the subnet's boot files: a client whose user class is
// iPXE gets the iPXE file, one of architecture 7 or 9 the UEFI file and one of architecture 0 the
// BIOS file, each with the server as next server. A client that runs iPXE gets the iPXE file
// alone, so that iPXE does not load itself again.
func bootClasses(s servedSubnet) []any {
	b := s.spec.Boot
	if b == nil {
		return nil
	}
	notIPXE := ""
	if b.IPXE != nil {
		notIPXE = "not member('" + s.name + "-ipxe') and "
	}
	var classes []any
	for _, c := range []struct {
		file       *string
		name, test string
	}{
		{b.IPXE, "ipxe", "substring(option[77].hex, 0, 4) == 'iPXE'"},
		{b.UEFI, "uefi", notIPXE + "(option[93].hex == 0x0007 or option[93].hex == 0x0009)"},
		{b.BIOS, "bios", notIPXE + "option[93].hex == 0x0000"},
	} {
		if c.file == nil {
			continue
		}
		class := map[string]any{
			"name":                    s.name + "-" + c.name,
			"only-in-additional-list": true,
			"test":                    c.test,
			"boot-file-name":          *c.file,
		}
		if b.Server != nil {
			class["next-server"] = netip.MustParseAddr(*b.Server).String()
		}
		classes = append(classes, class)
	}
	return classes
}
