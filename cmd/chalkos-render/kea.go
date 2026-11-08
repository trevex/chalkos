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

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/services"
)

// The output kea: the config of Kea's DHCPv4 and DHCPv6 servers of each VRF whose links
// DHCPSubnet documents serve, on the links the subnets name. The kind's rules check each subnet
// on its own, and the validator router, with derive.Model.Subnets, what spans documents: one VRF
// per subnet, one subnet of each family per link, no overlap within a server, the router's
// addresses in each subnet, what a subnet delegates, and router advertisements that send IPv6
// clients to the DHCPv6 server. The renderer refuses two subnets of a server that get one number
// in Kea. The output's check runs Kea's own, kea-dhcp4 -t and kea-dhcp6 -t, on each config,
// which refuses pools that run backwards, overlap or leave the subnet, a reservation outside the
// subnet and a lease past 4294967295 seconds; the renderer guards what Kea's check passes and
// would panic, or what Kea would read as more values.

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
	if s.v6 {
		return services.Kea6.File(s.vrf)
	}
	return services.Kea4.File(s.vrf)
}

// instance is the part of the server's control socket's and lease file's names: dhcp4 for the
// default VRF's DHCPv4 server, dhcp6-<vrf> for a VRF's DHCPv6 server. Kea takes sockets in
// /run/kea alone and lease files in /var/lib/kea alone, so the servers share both directories.
func (s keaServer) instance() string {
	if s.vrf == derive.DefaultVRF {
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

// servedSubnet is a subnet as the model serves it, with Kea's number for it and its lease time.
type servedSubnet struct {
	derive.Subnet
	// id is Kea's number for the subnet, unique within its server: an IPv4 subnet's first address
	// as an integer, as no two subnets of a VRF overlap, and a hash of an IPv6 subnet; a subnet
	// keeps its number, and its leases, while documents come and go or are renamed.
	id uint32
	// seconds is the lease time.
	seconds uint64
}

// server is the Kea server that serves the subnet.
func (s servedSubnet) server() keaServer {
	return keaServer{vrf: s.VRF, v6: s.Is6()}
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
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	subnets, err := d.Subnets()
	if err != nil {
		return err
	}
	byServer := map[keaServer][]servedSubnet{}
	for _, s := range subnets {
		sub, err := keaSubnetOf(s)
		if err != nil {
			return derive.Within("DHCPSubnet "+s.Name, err)
		}
		for _, other := range byServer[sub.server()] {
			if err := numberedApart(other, sub); err != nil {
				return err
			}
		}
		byServer[sub.server()] = append(byServer[sub.server()], sub)
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

// numberedApart refuses b, a subnet of the server of a, whose number in Kea is a's, as two hashes
// of IPv6 subnets may be.
func numberedApart(a, b servedSubnet) error {
	if a.id == b.id {
		return refusals.Errorf("DHCPSubnet", b.Name, "spec.subnet", "DHCPSubnet %s and DHCPSubnet %s get the same number %d in Kea; change one subnet's range", a.Name, b.Name, b.id)
	}
	return nil
}

// keaSubnetOf is the subnet s with Kea's number for it and its lease time in seconds, once
// keaValues has guarded the values Kea's config takes as they are.
func keaSubnetOf(s derive.Subnet) (servedSubnet, error) {
	sub := servedSubnet{Subnet: s}
	if err := keaValues(s.Spec); err != nil {
		return sub, err
	}
	// Kea's check refuses the number 0, of a subnet at 0.0.0.0, which the rules refuse first.
	if s.Is6() {
		sub.id = subnetID6(s.Prefix)
	} else {
		sub.id = binary.BigEndian.Uint32(s.Prefix.Addr().AsSlice())
	}
	var err error
	sub.seconds, err = leaseSeconds(s.Spec.LeaseTime)
	return sub, err
}

// keaValues guards, with an internal error, the values of the subnet s that Kea's config takes as
// they are and Kea's check passes: an address that does not parse, which netip.MustParseAddr
// panics on, or that has a zone, which netip takes of any characters and Kea's option data splits
// at a comma into more values, and a domain that is no DNS name, which Kea's option data splits
// as well. The schema refuses each of them.
func keaValues(s derive.DHCPSubnetSpec) error {
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
			return derive.InternalErrorf("%q is no address without a zone", a)
		}
	}
	if dom := s.Domain; dom != nil && !dhcpName.MatchString(*dom) {
		return derive.InternalErrorf("%q is no domain", *dom)
	}
	return nil
}

// leaseSeconds is a lease time of the schema in seconds. It guards, with an internal error, a
// time the schema refuses, which it reads no seconds of. Kea's check refuses more than 4294967295
// seconds, which the rules refuse first.
func leaseSeconds(t string) (uint64, error) {
	m := leaseTime.FindStringSubmatch(t)
	if m == nil {
		return 0, derive.InternalErrorf("leaseTime %q is no number and s, m, h or d", t)
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
		for _, l := range s.Links {
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
	for _, p := range s.Pools {
		pools = append(pools, map[string]any{"pool": p[0].String() + " - " + p[1].String()})
	}
	var options []any
	option := func(name, data string) {
		options = append(options, map[string]any{"name": name, "data": data})
	}
	if s.Spec.Gateway != nil {
		option("routers", netip.MustParseAddr(*s.Spec.Gateway).String())
	}
	if len(s.Spec.DNS) > 0 {
		var dns []string
		for _, a := range s.Spec.DNS {
			dns = append(dns, netip.MustParseAddr(a).String())
		}
		option("domain-name-servers", strings.Join(dns, ", "))
	}
	if s.Spec.Domain != nil {
		option("domain-name", *s.Spec.Domain)
		option("domain-search", *s.Spec.Domain)
	}
	var reservations []any
	for _, r := range s.Spec.Reservations {
		h := map[string]any{"hw-address": r.MAC, "ip-address": netip.MustParseAddr(r.Address).String()}
		if r.Hostname != nil {
			h["hostname"] = *r.Hostname
		}
		reservations = append(reservations, h)
	}
	subnet := map[string]any{
		"id":             s.id,
		"subnet":         s.Prefix.String(),
		"valid-lifetime": s.seconds,
		// chalkd and the diagnostic dhcp-leases name a subnet by its document.
		"user-context": map[string]any{"name": s.Name},
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
	b := s.Spec.Boot
	if b == nil {
		return nil
	}
	var names []string
	for _, c := range []struct {
		file *string
		name string
	}{{b.IPXE, "ipxe"}, {b.UEFI, "uefi"}, {b.BIOS, "bios"}} {
		if c.file != nil {
			names = append(names, s.Name+"-"+c.name)
		}
	}
	return names
}

// bootClasses are the client classes of the subnet's boot files: a client whose user class is
// iPXE gets the iPXE file, one of architecture 7 or 9 the UEFI file and one of architecture 0 the
// BIOS file, each with the server as next server. A client that runs iPXE gets the iPXE file
// alone, so that iPXE does not load itself again.
func bootClasses(s servedSubnet) []any {
	b := s.Spec.Boot
	if b == nil {
		return nil
	}
	notIPXE := ""
	if b.IPXE != nil {
		notIPXE = "not member('" + s.Name + "-ipxe') and "
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
			"name":                    s.Name + "-" + c.name,
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
