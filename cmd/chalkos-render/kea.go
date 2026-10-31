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

// The output kea: the config of Kea's DHCPv4 server of each VRF whose links DHCPSubnet documents
// serve, on the links the subnets name. The renderer checks across documents what the kind's rules
// cannot: one VRF per subnet, one subnet per link, no overlap within a VRF, a static address of
// the router in the subnet on each link, the order of the pools' and reservations' addresses, and
// pools and reservations apart from the router's, the gateway's and the DNS and boot servers'
// addresses.

// keaFile is the file renderKea writes for the default VRF's server; a VRF's server gets
// kea-dhcp4-<vrf>.json.
const keaFile = "kea-dhcp4.json"

// keaFileOf is the config file of the server of vrf.
func keaFileOf(vrf string) string {
	if vrf == defaultVRF {
		return keaFile
	}
	return "kea-dhcp4-" + vrf + ".json"
}

// keaInstanceOf is the part of the server of vrf's control socket's and lease file's names:
// dhcp4 for the default VRF's, dhcp4-<vrf> for a VRF's. Kea takes sockets in /run/kea alone and
// lease files in /var/lib/kea alone, so the servers share both directories.
func keaInstanceOf(vrf string) string {
	if vrf == defaultVRF {
		return "dhcp4"
	}
	return "dhcp4-" + vrf
}

// keaRetryWait and keaRetries are how often Kea tries again to open the sockets of a link that is
// down or has no address yet, as at boot: every 5 seconds for as long as it runs.
const (
	keaRetryWait = 5000
	keaRetries   = math.MaxUint32
)

// macAddress is a MAC address as the schema takes it.
var macAddress = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// bootFile is a file a PXE client loads as the schema takes it.
var bootFile = regexp.MustCompile(`^[!-~]{1,127}$`)

// dhcpName is a DNS name as the schema takes a domain and a reservation's hostname.
var dhcpName = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)*[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// leaseTime is a lease's duration as the schema takes it.
var leaseTime = regexp.MustCompile(`^([1-9][0-9]{0,5})([smhd])$`)

// servedSubnet is a DHCPSubnet with the links it is served on and the addresses it was checked to.
type servedSubnet struct {
	name  string
	spec  dhcpSubnetSpec
	links []string
	vrf   string
	// prefix is the subnet, masked, and id Kea's number for it: its first address as an integer,
	// unique within a VRF as no two subnets of a VRF overlap, so a subnet keeps its leases while
	// documents come and go or are renamed.
	prefix netip.Prefix
	id     uint32
	pools  [][2]netip.Addr
	// seconds is the lease time.
	seconds uint64
}

// renderKea writes Kea's DHCPv4 config of each VRF whose links DHCPSubnet documents serve,
// out/kea-dhcp4.json for the default VRF and out/kea-dhcp4-<vrf>.json for a VRF. A node without a
// DHCPSubnet gets no file and runs no server.
func renderKea(in map[string]any, out string) error {
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
	served := map[string]string{}
	byVRF := map[string][]servedSubnet{}
	for _, s := range d.DHCPSubnets {
		sub, err := serveSubnet(d, units, devices, s)
		if err != nil {
			return fmt.Errorf("DHCPSubnet %s: %w", s.Name, err)
		}
		for _, l := range sub.links {
			if other, ok := served[l]; ok {
				return fmt.Errorf("DHCPSubnet %s and DHCPSubnet %s both serve %s; a link takes one subnet", other, s.Name, l)
			}
			served[l] = s.Name
		}
		for _, other := range byVRF[sub.vrf] {
			if other.prefix.Overlaps(sub.prefix) {
				return fmt.Errorf("DHCPSubnet %s and DHCPSubnet %s overlap in VRF %s: %s and %s", other.name, s.Name, sub.vrf, other.prefix, sub.prefix)
			}
		}
		byVRF[sub.vrf] = append(byVRF[sub.vrf], sub)
	}
	vrfs := slices.Collect(maps.Keys(byVRF))
	slices.SortFunc(vrfs, func(a, b string) int { return strings.Compare(vrfOrder(a), vrfOrder(b)) })
	for _, vrf := range vrfs {
		data, err := json.MarshalIndent(keaConfig(vrf, byVRF[vrf]), "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(out, keaFileOf(vrf), append(data, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// serveSubnet checks the subnet s and finds its links, their VRF and the router's addresses in it.
func serveSubnet(d routerDocuments, units []networkUnit, devices []string, s named[dhcpSubnetSpec]) (servedSubnet, error) {
	sub := servedSubnet{name: s.Name, spec: s.Spec}
	p, err := netip.ParsePrefix(s.Spec.Subnet)
	if err != nil || !p.Addr().Is4() || p.Bits() > 30 {
		return sub, fmt.Errorf("the subnet %q is no IPv4 range of 30 bits at most", s.Spec.Subnet)
	}
	sub.prefix = p.Masked()
	sub.id = binary.BigEndian.Uint32(sub.prefix.Addr().AsSlice())
	if sub.id == 0 {
		return sub, fmt.Errorf("the subnet %s starts at 0.0.0.0, which Kea numbers no subnet by", sub.prefix)
	}
	if sub.links, sub.vrf, err = subnetLinks(d, devices, s.Spec.Interfaces); err != nil {
		return sub, err
	}
	own, err := ownAddresses(d, units, sub)
	if err != nil {
		return sub, err
	}
	// taken are the addresses no pool or reservation holds, each with what it is: the router's own
	// on the subnet's links, the gateway and the DNS and boot servers.
	var taken []takenAddress
	for _, o := range own {
		taken = append(taken, takenAddress{o, "the router's own address on the subnet's links"})
	}
	if g := s.Spec.Gateway; g != nil {
		a, err := netip.ParseAddr(*g)
		if err != nil || !a.Is4() || !sub.prefix.Contains(a) {
			return sub, fmt.Errorf("the gateway %q is no address of the subnet %s", *g, sub.prefix)
		}
		if a == sub.prefix.Addr() || a == broadcast(sub.prefix) {
			return sub, fmt.Errorf("the gateway, %s, is the network or broadcast address of the subnet %s", a, sub.prefix)
		}
		taken = append(taken, takenAddress{a, "the gateway"})
	}
	for _, a := range s.Spec.DNS {
		addr, err := netip.ParseAddr(a)
		if err != nil || !addr.Is4() {
			return sub, fmt.Errorf("the DNS server %q is no IPv4 address", a)
		}
		taken = append(taken, takenAddress{addr, "a DNS server"})
	}
	if b := s.Spec.Boot; b != nil {
		if b.Server != nil {
			a, err := netip.ParseAddr(*b.Server)
			if err != nil || !a.Is4() {
				return sub, fmt.Errorf("boot's server %q is no IPv4 address", *b.Server)
			}
			taken = append(taken, takenAddress{a, "boot's server"})
		}
		files := 0
		for _, f := range []*string{b.BIOS, b.UEFI, b.IPXE} {
			if f == nil {
				continue
			}
			if !bootFile.MatchString(*f) {
				return sub, fmt.Errorf("boot's file %q is no file of 1 to 127 printable characters without spaces", *f)
			}
			files++
		}
		if files == 0 {
			return sub, fmt.Errorf("boot names no file")
		}
	}
	// inSubnet parses an address of a pool or reservation, which lies in the subnet and is neither
	// its network's nor its broadcast address nor taken.
	inSubnet := func(what, a string) (netip.Addr, error) {
		addr, err := netip.ParseAddr(a)
		if err != nil || !addr.Is4() || !sub.prefix.Contains(addr) {
			return addr, fmt.Errorf("%s, %q, is no address of the subnet %s", what, a, sub.prefix)
		}
		if addr == sub.prefix.Addr() || addr == broadcast(sub.prefix) {
			return addr, fmt.Errorf("%s, %s, is the network or broadcast address of the subnet %s", what, addr, sub.prefix)
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
	if dom := s.Spec.Domain; dom != nil && !dhcpName.MatchString(*dom) {
		return sub, fmt.Errorf("%q is no domain", *dom)
	}
	if sub.seconds, err = leaseSeconds(s.Spec.LeaseTime); err != nil {
		return sub, err
	}
	return sub, nil
}

// takenAddress is an address of a subnet that no pool or reservation holds, and what it is.
type takenAddress struct {
	addr netip.Addr
	what string
}

// subnetLinks are the links the interfaces of a subnet serve it on, sorted, and their VRF. Kea
// reads and answers on raw sockets ahead of the firewall, so a subnet names each of its links: it
// refuses a glob, as the schema does, an interface that is a VRF's device or a NAT64's link, links
// of two VRFs and links in a VRF of a Network's VRF= that no VRF document declares.
func subnetLinks(d routerDocuments, devices []string, interfaces []string) ([]string, string, error) {
	if len(interfaces) == 0 {
		return nil, "", fmt.Errorf("interfaces names no interface")
	}
	var links []string
	for _, i := range interfaces {
		if !plainInterface.MatchString(i) {
			if interfaceName.MatchString(i) {
				return nil, "", fmt.Errorf("interfaces: %q is a glob; a subnet names each link it is served on", i)
			}
			return nil, "", fmt.Errorf("interfaces: %q is no interface name", i)
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
			return nil, "", fmt.Errorf("its links are in VRF %s and VRF %s; a subnet is served in one VRF", vrf, v)
		}
		vrf = v
	}
	if vrf != defaultVRF && !slices.ContainsFunc(d.VRFs, func(v named[vrfSpec]) bool { return v.Name == vrf }) {
		return nil, "", fmt.Errorf("its links are in the VRF %q, which no VRF document declares", vrf)
	}
	return links, vrf, nil
}

// ownAddresses are the router's static addresses in the subnet on its links, which the Network
// networkd picks for each link gives. Kea answers a client on a link from the link's address in
// the subnet, and leases no address of a link without one, so it refuses a link without one.
func ownAddresses(d routerDocuments, units []networkUnit, sub servedSubnet) ([]netip.Addr, error) {
	var own []netip.Addr
	for _, l := range sub.links {
		cs := candidates(units, l)
		if len(cs) == 0 || cs[0].narrowed {
			return nil, fmt.Errorf("no Network document matches %s by Name= alone, so the renderer cannot find its address in the subnet %s; give the link one", l, sub.prefix)
		}
		found := false
		for _, a := range d.Addresses {
			if a.network == cs[0].name && a.prefix.Addr().Is4() && sub.prefix.Contains(a.prefix.Addr()) {
				own, found = appendNewAddr(own, a.prefix.Addr()), true
			}
		}
		if !found {
			return nil, fmt.Errorf("Network %s gives %s no static address in the subnet %s, which Kea answers its clients from", cs[0].name, l, sub.prefix)
		}
	}
	return own, nil
}

// broadcast is the last address of the IPv4 range p.
func broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	n := binary.BigEndian.Uint32(a[:]) | (1<<(32-p.Bits()) - 1)
	binary.BigEndian.PutUint32(a[:], n)
	return netip.AddrFrom4(a)
}

// appendNewAddr appends a to l unless l holds it.
func appendNewAddr(l []netip.Addr, a netip.Addr) []netip.Addr {
	if slices.Contains(l, a) {
		return l
	}
	return append(l, a)
}

// leaseSeconds is a lease time of the schema in seconds, which Kea takes as a 32-bit number.
func leaseSeconds(t string) (uint64, error) {
	m := leaseTime.FindStringSubmatch(t)
	if m == nil {
		return 0, fmt.Errorf("leaseTime %q is no number and s, m, h or d", t)
	}
	n, _ := strconv.ParseUint(m[1], 10, 64)
	n *= map[string]uint64{"s": 1, "m": 60, "h": 3600, "d": 86400}[m[2]]
	if n > math.MaxUint32 {
		return 0, fmt.Errorf("leaseTime %s is longer than %d seconds, the longest Kea takes", t, uint64(math.MaxUint32))
	}
	return n, nil
}

// keaConfig is the config of the DHCPv4 server of vrf: raw sockets on the subnets' links, its
// control socket with the lease commands, its leases in a file, a client class of each subnet's
// boot file and the subnets.
func keaConfig(vrf string, subnets []servedSubnet) map[string]any {
	instance := keaInstanceOf(vrf)
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
		// Kea's messages go to the journal, which stamps them. Each command on the control socket
		// logs a warning of the dhcp logger, and the commands logger names each, which chalkd's
		// questions would fill the journal with.
		"loggers": []any{
			keaLogger("kea-dhcp4", "INFO"),
			keaLogger("kea-dhcp4.dhcp", "ERROR"),
			keaLogger("kea-dhcp4.commands", "WARN"),
		},
	}
	if len(classes) > 0 {
		dhcp4["client-classes"] = classes
	}
	return map[string]any{"Dhcp4": dhcp4}
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
