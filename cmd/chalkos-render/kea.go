package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/keaconf"
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

// svc is the service of the server's family, whose name is its program's and its logger's.
func (s keaServer) svc() services.Service {
	if s.v6 {
		return services.Kea6
	}
	return services.Kea4
}

// file is the server's config file: kea-dhcp4.json for the default VRF's DHCPv4 server,
// kea-dhcp6-<vrf>.json for a VRF's DHCPv6 server.
func (s keaServer) file() string {
	return s.svc().File(s.vrf)
}

// socket is the server's control socket, <instance>.sock in its runtime directory.
func (s keaServer) socket() string {
	return "/run/" + s.svc().RuntimeDir(s.vrf) + "/" + s.instance() + ".sock"
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
		return strings.Compare(derive.VRFDevice(a.vrf)+" "+a.family(), derive.VRFDevice(b.vrf)+" "+b.family())
	})
	for _, srv := range servers {
		var config keaconf.Config
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

// keaSubnetOf is the subnet s with Kea's number for it and its lease time in seconds. It guards,
// with an internal error, a domain that is no DNS name, which Kea's config takes as it is, Kea's
// check passes and Kea's option data splits at a comma into more values. The schema refuses it.
func keaSubnetOf(s derive.Subnet) (servedSubnet, error) {
	sub := servedSubnet{Subnet: s}
	if dom := s.Spec.Domain; dom != nil && !dhcpName.MatchString(*dom) {
		return sub, derive.InternalErrorf("%q is no domain", *dom)
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
func keaConfig(srv keaServer, subnets []servedSubnet) keaconf.Config {
	var links []string
	var classes []keaconf.ClientClass
	var subnet4 []keaconf.Subnet4
	for _, s := range subnets {
		for _, l := range s.Links {
			links = appendNew(links, l)
		}
		classes = append(classes, bootClasses(s)...)
		subnet4 = append(subnet4, keaSubnet4(s))
	}
	slices.Sort(links)
	server := keaServerConfig(srv, links, nil)
	// Raw sockets take the requests of clients without an address, and send the replies through
	// the link, ahead of the firewall.
	server.InterfacesConfig.DHCPSocketType = "raw"
	return keaconf.Config{Dhcp4: &keaconf.Dhcp4{Server: server, ClientClasses: classes, Subnet4: subnet4}}
}

// keaServerConfig is what the configs of both families of the server srv share: its links, its
// control socket, its leases in a file, the lease commands before the hooks, and its loggers.
func keaServerConfig(srv keaServer, links []string, hooks []keaconf.Hook) keaconf.Server {
	return keaconf.Server{
		InterfacesConfig: keaconf.InterfacesConfig{
			Interfaces:    links,
			RetryWaitTime: keaRetryWait,
			MaxRetries:    keaRetries,
		},
		ControlSockets: []keaconf.ControlSocket{{SocketType: "unix", SocketName: srv.socket()}},
		LeaseDatabase:  &keaconf.LeaseDatabase{Type: "memfile", Persist: true, Name: "/var/lib/kea/" + srv.instance() + ".leases", LFCInterval: 3600},
		HooksLibraries: append([]keaconf.Hook{{Library: "libdhcp_lease_cmds.so"}}, hooks...),
		Loggers:        keaLoggers(srv),
	}
}

// keaLoggers are the loggers of the server srv: Kea's messages go to the journal, which stamps
// them. Each command on the control socket logs a warning of the dhcp logger, and the commands
// logger names each, which chalkd's questions would fill the journal with.
func keaLoggers(srv keaServer) []keaconf.Logger {
	name := srv.svc().Name
	return []keaconf.Logger{
		keaLogger(name, "INFO"),
		keaLogger(name+".dhcp", "ERROR"),
		keaLogger(name+".commands", "WARN"),
	}
}

// keaLogger logs the messages of the logger name from severity up to the journal.
func keaLogger(name, severity string) keaconf.Logger {
	return keaconf.Logger{
		Name:          name,
		Severity:      severity,
		OutputOptions: []keaconf.OutputOption{{Output: "stdout", Pattern: "%-5p [%c] %m\n"}},
	}
}

// keaSubnet4 is the subnet s as Kea's subnet4 takes it, with the client classes of its boot files.
func keaSubnet4(s servedSubnet) keaconf.Subnet4 {
	var options []keaconf.Option
	if s.Spec.Gateway != nil {
		options = append(options, keaconf.Option{Name: "routers", Data: s.Spec.Gateway.String()})
	}
	options = append(options, dnsOption("domain-name-servers", s)...)
	if s.Spec.Domain != nil {
		options = append(options,
			keaconf.Option{Name: "domain-name", Data: *s.Spec.Domain},
			keaconf.Option{Name: "domain-search", Data: *s.Spec.Domain})
	}
	return keaconf.Subnet4{Subnet: keaSubnet(s, options), EvaluateAdditionalClasses: bootClassNames(s)}
}

// keaSubnet is what Kea's subnet4 and subnet6 take alike of the subnet s: its number, range,
// lease time, pools, options and reservations. chalkd and the diagnostic dhcp-leases name a
// subnet by its document.
func keaSubnet(s servedSubnet, options []keaconf.Option) keaconf.Subnet {
	sub := keaconf.Subnet{
		ID:            s.id,
		Subnet:        s.Prefix.String(),
		ValidLifetime: s.seconds,
		UserContext:   keaconf.UserContext{Name: s.Name},
		OptionData:    options,
	}
	for _, p := range s.Pools {
		sub.Pools = append(sub.Pools, keaconf.Pool{Pool: p[0].String() + " - " + p[1].String()})
	}
	for _, r := range s.Spec.Reservations {
		h := keaconf.Reservation{HWAddress: r.MAC, Hostname: r.Hostname}
		if s.Is6() {
			h.IPAddresses = []string{r.Address.String()}
		} else {
			h.IPAddress = r.Address.String()
		}
		sub.Reservations = append(sub.Reservations, h)
	}
	return sub
}

// dnsOption is the option name of the subnet's DNS servers, none without servers.
func dnsOption(name string, s servedSubnet) []keaconf.Option {
	if len(s.Spec.DNS) == 0 {
		return nil
	}
	var dns []string
	for _, a := range s.Spec.DNS {
		dns = append(dns, a.String())
	}
	return []keaconf.Option{{Name: name, Data: strings.Join(dns, ", ")}}
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
func bootClasses(s servedSubnet) []keaconf.ClientClass {
	b := s.Spec.Boot
	if b == nil {
		return nil
	}
	notIPXE := ""
	if b.IPXE != nil {
		notIPXE = "not member('" + s.Name + "-ipxe') and "
	}
	var classes []keaconf.ClientClass
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
		class := keaconf.ClientClass{
			Name:                 s.Name + "-" + c.name,
			OnlyInAdditionalList: true,
			Test:                 c.test,
			BootFileName:         *c.file,
		}
		if b.Server != nil {
			class.NextServer = b.Server.String()
		}
		classes = append(classes, class)
	}
	return classes
}
