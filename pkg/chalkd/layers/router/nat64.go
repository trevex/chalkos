package router

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/poll"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
)

const (
	// nat64Protocol is the protocol of the routes to the NAT64s' links, which BGP announces when a
	// network names it.
	nat64Protocol = 64
	// nat64Peer is the end of each NAT64's veth pair in the instance's namespace.
	nat64Peer = "nat64"
	// nat64LinkPrefix starts the name of each NAT64's veth pair's end in the router's namespace.
	nat64LinkPrefix = "n64-"
	// nat64NamespacePrefix starts the name of each NAT64's network namespace.
	nat64NamespacePrefix = "nat64-"
	// vrfTableWait is how long a NAT64 waits for its VRF to have the rendered table: networkd
	// creates a VRF without waiting, at boot and when a delivery changed its table.
	vrfTableWait = 30 * time.Second
)

var (
	// vethVRF is a VRF's name in a link's file, an interface's name as chalkos-render writes it.
	vethVRF = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$`)
	// vethRoute is an IPv4 route of a link's file.
	vethRoute = regexp.MustCompile(`^[0-9]{1,3}([.][0-9]{1,3}){3}/[0-9]{1,2}$`)
)

// nat64Instance is a NAT64 the output jool renders: Jool's file <name>.json and its link's file
// <name>.veth.
type nat64Instance struct {
	name, file string
	// pool6 is the instance's prefix.
	pool6 string
	// vrf is the VRF of its link, empty for the default one, whose table is table.
	vrf, table string
	// outer and inner are the addresses of the link's ends, in the router's and in the
	// instance's namespace.
	outer, inner string
	// routes are the IPv4 routes of its pool.
	routes []netip.Prefix
}

// link is the end of the instance's veth pair in the router's namespace.
func (n nat64Instance) link() string { return nat64LinkPrefix + n.name }

// readNAT64 reads the NAT64 of Jool's file file and its link's file beside it, after jool file
// check took Jool's file, as jool file handle reads it.
func (p *Plugin) readNAT64(ctx context.Context, file string) (nat64Instance, error) {
	n := nat64Instance{name: strings.TrimSuffix(filepath.Base(file), ".json"), file: file}
	veth := filepath.Join(filepath.Dir(file), n.name+".veth")
	refused := fmt.Errorf("jool refuses %s, or %s is no link's file; no instance changed", file, veth)
	if _, err := tool.Run(ctx, p.run, "jool", "file", "check", file); err != nil {
		return n, refused
	}
	if err := n.readLink(veth); err != nil {
		return n, refused
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return n, err
	}
	var j struct {
		Global struct {
			Pool6 string `json:"pool6"`
		} `json:"global"`
	}
	if err := json.Unmarshal(data, &j); err != nil || j.Global.Pool6 == "" {
		return n, fmt.Errorf("%s names no pool6; no instance changed", file)
	}
	n.pool6 = j.Global.Pool6
	return n, nil
}

// readLink reads the link's file veth, of the form chalkos-render writes.
func (n *nat64Instance) readLink(veth string) error {
	data, err := os.ReadFile(veth)
	if err != nil {
		return err
	}
	var v struct {
		Table  json.RawMessage `json:"table"`
		VRF    *string         `json:"vrf"`
		Outer  *string         `json:"outer"`
		Inner  *string         `json:"inner"`
		Routes *[]string       `json:"routes"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.Table == nil || v.Outer == nil || v.Inner == nil || v.Routes == nil {
		return errors.New("a field is missing")
	}
	table, err := strconv.ParseUint(string(v.Table), 10, 32)
	if err != nil {
		return err
	}
	if v.VRF != nil && !vethVRF.MatchString(*v.VRF) {
		return fmt.Errorf("VRF %q", *v.VRF)
	}
	for _, r := range *v.Routes {
		p, err := netip.ParsePrefix(r)
		if !vethRoute.MatchString(r) || err != nil {
			return fmt.Errorf("route %q", r)
		}
		n.routes = append(n.routes, p)
	}
	n.table = strconv.FormatUint(table, 10)
	if v.VRF != nil {
		n.vrf = *v.VRF
	}
	n.outer, n.inner = *v.Outer, *v.Inner
	return nil
}

// reconcileNAT64 brings Jool's instances, their namespaces and links and the routes to them to the files
// of the output jool, or with stop removes them all, as a missing output directory (maintenance
// mode) does. Every file is checked first, so a file jool or the link's form refuses, or a pool
// holding an address of a router link, changes no instance. Each instance runs in the namespace
// nat64-<name>, joined to its VRF by the veth pair n64-<name> (end nat64 inside): Jool's hook takes
// the packets of every link of its namespace, so in the router's own it would translate other
// VRFs' packets too. The namespace forwards nothing: Jool sends what it translates, and a packet
// it leaves alone would loop back until its hop limit ran out. An instance is created or replaced
// in place, keeping its sessions, except on a new pool6, which Jool cannot change: then it is
// removed first and loses them. The VRF's table routes the prefix and an explicit pool to the
// link with protocol 64. Protocol-64 routes, links and namespaces no file names are removed in
// that order, so no route goes with its link first. A failed step does not stop the others; the
// run then fails naming each. A failed instance keeps its routes.
func (p *Plugin) reconcileNAT64(ctx context.Context, stop bool, stderr io.Writer) error {
	refuse := func(err error) error {
		fmt.Fprintf(stderr, "chalkd router nat64: %v\n", err)
		return cli.Status(1)
	}
	var files []string
	if fi, err := os.Stat(p.NAT64Dir); !stop && err == nil && fi.IsDir() {
		files, _ = filepath.Glob(filepath.Join(p.NAT64Dir, "*.json"))
	}
	var instances []nat64Instance
	// keep holds "<table> <destination>" of each route a file names.
	keep := map[string]bool{}
	for _, f := range files {
		n, err := p.readNAT64(ctx, f)
		if err != nil {
			return refuse(err)
		}
		instances = append(instances, n)
		keep[n.table+" "+n.pool6] = true
		for _, r := range n.routes {
			keep[n.table+" "+r.String()] = true
		}
	}
	if err := p.checkPools(instances); err != nil {
		return refuse(err)
	}
	var failed []string
	// Whether a VRF had its table, by "<vrf> <table>", once a NAT64 waited for it.
	tabled := map[string]bool{}
	for _, n := range instances {
		if err := p.plumbNAT64(ctx, n, tabled, stderr); err != nil {
			failed = append(failed, "bring up the namespace and link of "+n.name)
			continue
		}
		failed = append(failed, p.applyNAT64(ctx, n)...)
	}
	failed = append(failed, p.removeStaleNAT64(ctx, instances, keep)...)
	for _, f := range failed {
		fmt.Fprintf(stderr, "chalkd router nat64: failed to %s\n", f)
	}
	if len(failed) > 0 {
		return cli.Status(1)
	}
	return nil
}

// checkPools refuses a pool that holds an address of one of the router's links, such as one from
// DHCP, which the rules cannot see: the router answers its own addresses before any route to a
// NAT64's link, so the pool would translate nothing.
func (p *Plugin) checkPools(instances []nat64Instance) error {
	if !slices.ContainsFunc(instances, func(n nat64Instance) bool { return len(n.routes) > 0 }) {
		return nil
	}
	hosts, err := p.IPv4Addrs("")
	if err != nil {
		return errors.New("cannot list the router's addresses; no instance changed")
	}
	for _, n := range instances {
		for _, r := range n.routes {
			for _, h := range hosts {
				if r.Contains(h.Prefix.Addr()) {
					return fmt.Errorf("NAT64 %s: the pool entry %s holds %s, an address of the link %s, which the router answers itself; leave pool4 empty to take the address of the link the connections leave through; no instance changed", n.name, r, h.Prefix.Addr(), h.Link)
				}
			}
		}
	}
	return nil
}

// netns is the path of the network namespace of the NAT64 name.
func (p *Plugin) netns(name string) string {
	return filepath.Join(p.NetnsDir, nat64NamespacePrefix+name)
}

// inside runs a command in the network namespace of the NAT64 name.
func (p *Plugin) inside(ctx context.Context, name string, args ...string) ([]byte, error) {
	return tool.Run(ctx, p.run, "nsenter", append([]string{"--net=" + p.netns(name)}, args...)...)
}

// insideQuiet runs a command in the network namespace of the NAT64 name, whose standard error
// reaches no one.
func (p *Plugin) insideQuiet(ctx context.Context, name string, args ...string) ([]byte, error) {
	return p.run.Exec(ctx, tool.Cmd{Name: "nsenter", Args: append([]string{"--net=" + p.netns(name)}, args...), Quiet: true})
}

// plumbNAT64 brings up the namespace of the NAT64 n and its veth pair. A link whose peer left the
// namespace (a namespace removed while a process held it) is made anew; a device of its name and
// another kind fails the instance, as does a VRF without the file's table after 30 s, with the
// VRF's other instances.
func (p *Plugin) plumbNAT64(ctx context.Context, n nat64Instance, tabled map[string]bool, stderr io.Writer) error {
	ip := func(args ...string) error {
		_, err := tool.Run(ctx, p.run, "ip", args...)
		return err
	}
	in := func(args ...string) error {
		_, err := p.inside(ctx, n.name, args...)
		return err
	}
	link := n.link()
	ns := nat64NamespacePrefix + n.name
	if _, err := os.Stat(p.netns(n.name)); err != nil {
		if err := ip("netns", "add", ns); err != nil {
			return err
		}
	}
	// A namespace takes the router's IPv4 forwarding when it is created.
	if err := in("sysctl", "-q", "-w", "net.ipv4.conf.all.forwarding=0", "net.ipv4.conf.default.forwarding=0",
		"net.ipv6.conf.all.forwarding=0", "net.ipv6.conf.default.forwarding=0"); err != nil {
		return err
	}
	links, err := p.Links()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(links, func(l routing.Link) bool { return l.Name == link })
	_, peerErr := p.insideQuiet(ctx, n.name, "ip", "link", "show", "dev", nat64Peer)
	// Either end without the other is no link of this namespace.
	if i < 0 || peerErr != nil {
		if i >= 0 {
			// A device of another kind is none of the unit's links.
			if kind := cmp.Or(links[i].Kind, "none"); kind != "veth" {
				fmt.Fprintf(stderr, "chalkd router nat64: %s is a device of kind %s, not a NAT64's link; remove or rename it\n", link, kind)
				return errors.New("a device of another kind")
			}
			if err := ip("link", "del", link); err != nil {
				return err
			}
		}
		if peerErr == nil {
			if err := in("ip", "link", "del", nat64Peer); err != nil {
				return err
			}
		}
		if err := ip("link", "add", link, "mtu", "65535", "type", "veth", "peer", "name", nat64Peer, "mtu", "65535", "netns", ns); err != nil {
			return err
		}
	}
	if n.vrf != "" {
		key := n.vrf + " " + n.table
		if _, tried := tabled[key]; !tried {
			tabled[key] = poll.Until(ctx, "", vrfTableWait, 200*time.Millisecond, func(context.Context) error {
				links, err := p.Links()
				if err != nil {
					return err
				}
				if !slices.ContainsFunc(links, func(l routing.Link) bool {
					return l.Name == n.vrf && l.Kind == "vrf" && strconv.FormatUint(uint64(l.Table), 10) == n.table
				}) {
					return errors.New("no table")
				}
				return nil
			}) == nil
		}
		if !tabled[key] {
			fmt.Fprintf(stderr, "chalkd router nat64: VRF %s has no table %s after %d s\n", n.vrf, n.table, int(vrfTableWait/time.Second))
			return errors.New("no table")
		}
		if err := ip("link", "set", "dev", link, "master", n.vrf); err != nil {
			return err
		}
	} else if err := ip("link", "set", "dev", link, "nomaster"); err != nil {
		return err
	}
	addrs, err := p.IPv4Addrs(link)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if a.Prefix.String() != n.outer {
			if err := ip("addr", "del", a.Prefix.String(), "dev", link); err != nil {
				return err
			}
		}
	}
	for _, args := range [][]string{
		{"addr", "replace", n.outer, "dev", link},
		{"-6", "addr", "replace", "fe80::1/64", "dev", link, "nodad"},
		{"link", "set", "dev", link, "up"},
	} {
		if err := ip(args...); err != nil {
			return err
		}
	}
	if err := in("ip", "link", "set", "dev", "lo", "up"); err != nil {
		return err
	}
	out, err := p.inside(ctx, n.name, "ip", "-4", "-j", "addr", "show", "dev", nat64Peer)
	if err != nil {
		return err
	}
	inner, err := ipAddrs(out)
	if err != nil {
		return err
	}
	for _, a := range inner {
		if a != n.inner {
			if err := in("ip", "addr", "del", a, "dev", nat64Peer); err != nil {
				return err
			}
		}
	}
	outer, _, _ := strings.Cut(n.outer, "/")
	for _, args := range [][]string{
		{"ip", "addr", "replace", n.inner, "dev", nat64Peer},
		{"ip", "-6", "addr", "replace", "fe80::2/64", "dev", nat64Peer, "nodad"},
		{"ip", "link", "set", "dev", nat64Peer, "up"},
		{"ip", "route", "replace", "default", "via", outer, "dev", nat64Peer},
		{"ip", "-6", "route", "replace", "default", "via", "fe80::1", "dev", nat64Peer},
	} {
		if err := in(args...); err != nil {
			return err
		}
	}
	return nil
}

// ipAddrs are the addresses, <local>/<prefixlen>, of ip -j addr show's output out.
func ipAddrs(out []byte) ([]string, error) {
	var links []struct {
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(out, &links); err != nil {
		return nil, fmt.Errorf("read ip's addresses: %w", err)
	}
	var addrs []string
	for _, l := range links {
		for _, a := range l.AddrInfo {
			addrs = append(addrs, a.Local+"/"+strconv.Itoa(a.PrefixLen))
		}
	}
	return addrs, nil
}

// applyNAT64 creates or replaces the instance of the NAT64 n, whose namespace and link are up,
// and routes its prefix and pool to its link. It returns what failed.
func (p *Plugin) applyNAT64(ctx context.Context, n nat64Instance) []string {
	var failed []string
	// Jool cannot change an instance's pool6.
	out, _ := p.insideQuiet(ctx, n.name, "jool", "-i", n.name, "global", "display", "--csv")
	var current []string
	for line := range strings.Lines(string(out)) {
		if v, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "pool6,"); ok {
			current = append(current, v)
		}
	}
	if cur := strings.Join(current, "\n"); cur != "" && cur != n.pool6 {
		if _, err := p.inside(ctx, n.name, "jool", "-i", n.name, "instance", "remove"); err != nil {
			failed = append(failed, fmt.Sprintf("remove instance %s of %s", n.name, cur))
		}
	}
	if _, err := p.inside(ctx, n.name, "jool", "file", "handle", n.file); err != nil {
		failed = append(failed, "apply instance "+n.name)
	}
	table := n.table
	if _, err := tool.Run(ctx, p.run, "ip", "-6", "route", "replace", n.pool6, "via", "fe80::2", "dev", n.link(), "table", table, "proto", strconv.Itoa(nat64Protocol)); err != nil {
		failed = append(failed, "route "+n.pool6)
	}
	inner, _, _ := strings.Cut(n.inner, "/")
	for _, r := range n.routes {
		if _, err := tool.Run(ctx, p.run, "ip", "-4", "route", "replace", r.String(), "via", inner, "dev", n.link(), "table", table, "proto", strconv.Itoa(nat64Protocol)); err != nil {
			failed = append(failed, "route "+r.String())
		}
	}
	return failed
}

// removeStaleNAT64 removes the protocol-64 routes keep does not hold, and the links and namespaces
// of NAT64s that are not among instances, in that order. It returns what failed.
func (p *Plugin) removeStaleNAT64(ctx context.Context, instances []nat64Instance, keep map[string]bool) []string {
	var failed []string
	want := map[string]bool{}
	for _, n := range instances {
		want[n.name] = true
	}
	for _, family := range []struct {
		flag string
		nl   int
	}{{"-4", netlink.FAMILY_V4}, {"-6", netlink.FAMILY_V6}} {
		routes, err := p.ProtocolRoutes(family.nl, nat64Protocol)
		if err != nil {
			failed = append(failed, "list routes")
		}
		for _, r := range routes {
			table, dst := strconv.FormatUint(uint64(r.Table), 10), r.Destination.String()
			if keep[table+" "+dst] {
				continue
			}
			if _, err := tool.Run(ctx, p.run, "ip", family.flag, "route", "del", dst, "table", table, "proto", strconv.Itoa(nat64Protocol)); err != nil {
				failed = append(failed, fmt.Sprintf("remove route %s of table %s", dst, table))
			}
		}
	}
	links, err := p.Links()
	if err != nil {
		failed = append(failed, "list links")
	}
	for _, l := range links {
		name, ok := strings.CutPrefix(l.Name, nat64LinkPrefix)
		if !ok || l.Kind != "veth" || want[name] {
			continue
		}
		if _, err := tool.Run(ctx, p.run, "ip", "link", "del", l.Name); err != nil {
			failed = append(failed, "remove the link "+l.Name)
		}
	}
	namespaces, _ := filepath.Glob(filepath.Join(p.NetnsDir, nat64NamespacePrefix+"*"))
	for _, ns := range namespaces {
		name := strings.TrimPrefix(filepath.Base(ns), nat64NamespacePrefix)
		if want[name] {
			continue
		}
		if _, err := tool.Run(ctx, p.run, "ip", "netns", "delete", nat64NamespacePrefix+name); err != nil {
			failed = append(failed, "remove the namespace of "+name)
		}
	}
	return failed
}
