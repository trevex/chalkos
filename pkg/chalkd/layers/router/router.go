// Package router is chalkd's router layer: it serves RouterService with the state of the node's
// FRR, Jool, Unbound and Kea and the kernel's routes.
package router

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// Options set up the router layer; zero fields mean chalkd's own.
type Options struct {
	// KernelRoutes lists the kernel's routes; nil reads them through netlink.
	KernelRoutes func() ([]*routing.KernelRoute, error)
	// FRRConfig is FRR's config, a link to the config frr.service fills in, which images with BGP
	// carry whether FRR runs or not; empty is /etc/frr/frr.conf. chalkd asks FRR nothing on an
	// image without it.
	FRRConfig string
	// ResolverConfig is Unbound's config, which exists on a node with a Resolver document;
	// empty is /etc/unbound/unbound.conf.
	ResolverConfig string
	// ResolverDir is the output unbound's directory, with unbound-<vrf>.conf per VRF's Resolver;
	// empty is /run/chalkos/outputs/unbound.
	ResolverDir string
	// KeaDir is the output kea's directory, with kea-dhcp4.json and kea-dhcp6.json for the default
	// VRF's DHCP servers and kea-dhcp4-<vrf>.json and kea-dhcp6-<vrf>.json per VRF's; empty is
	// /run/chalkos/outputs/kea.
	KeaDir string
	// PDDir holds the records of the prefixes Kea's DHCPv6 servers delegated; empty is
	// /var/lib/chalkos-dhcp6-pd.
	PDDir string
	// RelayDir is the output dhcp-relay's directory, with dnsmasq-relay.conf for the default
	// VRF's relay and dnsmasq-relay-<vrf>.conf per VRF's; empty is
	// /run/chalkos/outputs/dhcp-relay.
	RelayDir string
	// NAT64Dir is the output jool's directory, with a file <name>.json per NAT64 document; empty
	// is /run/chalkos/outputs/jool.
	NAT64Dir string
	// VRFDir holds the node's running VRF documents, <name>.json each; empty is
	// /run/chalkos/config/router.chalkos.dev/VRF.
	VRFDir string
	// RunDir holds each VRF's resolver's runtime directory, unbound-<vrf>, with the index of the
	// device the resolver's sockets are bound to, and the DHCP servers' kea, with their control
	// sockets and each VRF's server's index in dhcp4-<vrf>.ifindex or dhcp6-<vrf>.ifindex, and the
	// DHCP relays' dnsmasq-relay, with each VRF's relay's index in <vrf>.ifindex; empty is /run.
	RunDir string
	// NetDir holds a directory per network device, with its index; empty is /sys/class/net.
	NetDir string
}

// side is the node's router side: the state of its FRR, Jool, Unbound and Kea and the kernel's
// routes.
type side struct {
	// run runs vtysh, which FRR answers on its sockets in /run/frr, nsenter and jool,
	// unbound-control and systemctl.
	run tool.Runner
	Options
}

// Plugin is chalkd's router layer: it serves RouterService for the node's router side.
type Plugin struct{ r *side }

var _ layer.Plugin = (*Plugin)(nil)

// New is the router layer of the node, which runs vtysh, nsenter and jool, and unbound-control
// through the node's runner, and asks Kea on its control sockets.
func New(node layer.Node, o Options) *Plugin {
	return &Plugin{r: &side{run: node.Runner(), Options: o}}
}

func (p *Plugin) Name() string { return layers.Router }

func (p *Plugin) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return routerv1connect.NewRouterServiceHandler(p, opts...)
}

func (p *Plugin) Permissions() map[string]layer.Permission {
	return map[string]layer.Permission{
		routerv1connect.RouterServiceStatusProcedure: {Modes: layer.NormalOnly, Role: pki.RoleReader},
		routerv1connect.RouterServiceRoutesProcedure: {Modes: layer.NormalOnly, Role: pki.RoleReader},
	}
}

// Start runs nothing: the router's state is read when it is asked for.
func (p *Plugin) Start(context.Context) error { return nil }

func (p *Plugin) Status(ctx context.Context, _ *connect.Request[routerv1.StatusRequest]) (*connect.Response[routerv1.StatusResponse], error) {
	return connect.NewResponse(p.r.status(ctx)), nil
}

// askTimeout bounds the questions chalkd asks one router daemon, so Status answers while one
// hangs. Tests shorten it.
var askTimeout = 5 * time.Second

// ask runs one of the router's fixed questions to its daemons, whose standard error, while the
// daemon is down, goes into the error alone rather than into chalkd's log as well. budget is ctx
// bounded by askTimeout, which the questions to one daemon share.
func (r *side) ask(ctx, budget context.Context, program string, args ...string) ([]byte, error) {
	out, err := r.run.Exec(budget, tool.Cmd{Name: program, Args: args, Quiet: true})
	if err != nil {
		// nsenter runs the program that did not answer.
		who := program
		if program == "nsenter" && len(args) > 1 {
			who = args[1]
		}
		if why := ended(ctx, budget, who); why != nil {
			return nil, why
		}
	}
	return out, err
}

// isActive asks systemd whether unit is active, as systemctl.IsActive does, within budget as ask
// does.
func (r *side) isActive(ctx, budget context.Context, unit string) (string, error) {
	state, err := systemctl.IsActive(budget, r.run, unit)
	if err != nil {
		if why := ended(ctx, budget, "systemctl"); why != nil {
			return "", why
		}
	}
	return state, err
}

// ended is why a question to the daemon who failed when a context ended: the caller's own end,
// which is not the daemon's timeout, or the daemon's timeout once budget ran out. It is nil while
// neither ended, when the question failed for a reason of its own.
func ended(ctx, budget context.Context, who string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(budget.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s did not answer within %v", who, askTimeout)
	}
	return nil
}

// hasBGP reports whether the image runs BGP: whether it carries FRR's config link, which counts
// while FRR has not filled the config in. An error other than the link's absence counts as BGP,
// whose questions then say what is wrong.
func (r *side) hasBGP() bool {
	_, err := os.Lstat(cmp.Or(r.FRRConfig, "/etc/frr/frr.conf"))
	return !errors.Is(err, fs.ErrNotExist)
}

// vtysh runs one of the show commands of package router, which may carry a VRF's name that
// matched router.ShortName.
func (r *side) vtysh(ctx context.Context, command string) ([]byte, error) {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	return r.ask(ctx, budget, "vtysh", "-c", command)
}

// status is the state of the router: its BGP sessions, its NAT64 instances and its resolver, or
// why a daemon did not say; an image without BGP asks FRR nothing. The daemons are asked side by
// side, so daemons that hang cost one timeout altogether.
func (r *side) status(ctx context.Context) *routerv1.StatusResponse {
	st := &routerv1.StatusResponse{BgpAbsent: !r.hasBGP()}
	var wg sync.WaitGroup
	if !st.BgpAbsent {
		wg.Go(func() {
			out, err := r.vtysh(ctx, routing.NeighborsCommand)
			var peers []*routing.Peer
			if err == nil {
				peers, err = routing.ParsePeers(out)
			}
			if err != nil {
				st.BgpError = err.Error()
				return
			}
			for _, p := range peers {
				st.Peers = append(st.Peers, peerStatus(p))
			}
		})
	}
	wg.Go(func() {
		var err error
		if st.Nat64, err = r.nat64(ctx); err != nil {
			st.Nat64, st.Nat64Error = nil, err.Error()
		}
	})
	wg.Go(func() { st.Resolver = r.resolver(ctx) })
	wg.Go(func() { st.VrfResolvers = r.vrfResolvers(ctx) })
	wg.Go(func() { st.Dhcp = r.dhcpServers(ctx) })
	wg.Go(func() { st.DhcpRelays = r.dhcpRelays(ctx) })
	wg.Wait()
	return st
}

// nat64 is the state of the Jool instances of the node's NAT64 documents, all asked within one
// timeout; an instance removed meanwhile is left out.
func (r *side) nat64(ctx context.Context) ([]*routerv1.NAT64Status, error) {
	rendered, err := r.renderedNAT64()
	if err != nil || len(rendered) == 0 {
		return nil, err
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	out, err := r.ask(ctx, budget, "jool", routing.JoolInstancesArgs...)
	if err != nil {
		return nil, err
	}
	names, err := routing.ParseJoolInstances(out)
	if err != nil {
		return nil, err
	}
	var instances []*routerv1.NAT64Status
	for _, name := range names {
		// Jool lists the instances of every network namespace; the node's are those of its NAT64
		// documents, each in its namespace nat64-<name>.
		if !slices.Contains(rendered, name) {
			continue
		}
		n, err := r.nat64Instance(ctx, budget, name)
		if errors.Is(err, errInstanceGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		instances = append(instances, n)
	}
	return instances, nil
}

// errInstanceGone is a Jool instance removed while chalkd asks about it.
var errInstanceGone = errors.New("the instance is gone")

// nat64Instance is the state of one of Jool's instances: its counters of sessions and bindings
// and the size of each protocol's pool.
func (r *side) nat64Instance(ctx, budget context.Context, name string) (*routerv1.NAT64Status, error) {
	// Each instance runs in a network namespace of its own, and jool asks the namespace it runs in.
	jool := func(args []string) ([]byte, error) {
		out, err := r.ask(ctx, budget, "nsenter", routing.InNamespace(name, args)...)
		if te, ok := errors.AsType[*tool.Error](err); ok && (strings.Contains(te.Stderr, routing.JoolMissingInstance) || strings.Contains(te.Stderr, routing.NamespaceMissing)) {
			return nil, errInstanceGone
		}
		return out, err
	}
	out, err := jool(routing.JoolStatsArgs(name))
	if err != nil {
		return nil, err
	}
	n := &routerv1.NAT64Status{Name: name}
	if n.Sessions, n.Bindings, err = routing.ParseJoolStats(out); err != nil {
		return nil, fmt.Errorf("instance %s: %w", name, err)
	}
	for _, protocol := range routing.NAT64Protocols {
		out, err := jool(routing.JoolPool4Args(name, protocol))
		if err != nil {
			return nil, err
		}
		p := &routerv1.NAT64PoolStatus{Protocol: protocol}
		if p.Size, err = routing.ParseJoolPool4(out); err != nil {
			return nil, fmt.Errorf("instance %s: %w", name, err)
		}
		n.Pools = append(n.Pools, p)
	}
	return n, nil
}

// renderedNAT64 are the names of the node's NAT64 documents, whose instances the output jool
// renders as <name>.json.
func (r *side) renderedNAT64() ([]string, error) {
	entries, err := os.ReadDir(cmp.Or(r.NAT64Dir, filepath.Join(outputs.DefaultRoot, "jool")))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && e.Type().IsRegular() {
			names = append(names, name)
		}
	}
	return names, nil
}

// resolver is the state of Unbound on a node whose Resolver document rendered its config, and nil
// on another.
func (r *side) resolver(ctx context.Context) *routerv1.ResolverStatus {
	if _, err := os.Stat(cmp.Or(r.ResolverConfig, "/etc/unbound/unbound.conf")); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	if _, err := r.ask(ctx, budget, "unbound-control", "status"); err != nil {
		return &routerv1.ResolverStatus{Error: err.Error()}
	}
	return &routerv1.ResolverStatus{Running: true}
}

// vrfResolvers are the state of the unit unbound@<vrf> of each VRF whose config the output unbound
// rendered, sorted by VRF, all asked within one timeout. An active unit whose sockets are bound to
// no device of its VRF does not count as running.
func (r *side) vrfResolvers(ctx context.Context) []*routerv1.ResolverStatus {
	files, _ := filepath.Glob(filepath.Join(cmp.Or(r.ResolverDir, filepath.Join(outputs.DefaultRoot, "unbound")), "unbound-*.conf"))
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var resolvers []*routerv1.ResolverStatus
	for _, f := range files {
		vrf := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "unbound-"), ".conf")
		// Only a VRF's name reaches systemctl's command line.
		if !routing.ShortName.MatchString(vrf) {
			continue
		}
		unit := resolverUnit(vrf)
		state, err := r.isActive(ctx, budget, unit)
		s := &routerv1.ResolverStatus{Vrf: vrf, Running: err == nil}
		switch {
		case err == nil:
			if stale := r.staleDevice(vrf, filepath.Join(cmp.Or(r.RunDir, "/run"), "unbound-"+vrf, "ifindex")); stale != "" {
				s.Running, s.Error = false, unit+" is active, but "+stale
			}
		case state != "":
			s.Error = unit + " is " + state
		default:
			s.Error = err.Error()
		}
		resolvers = append(resolvers, s)
	}
	return resolvers
}

// staleDevice says why a running service of the VRF vrf, a resolver or a DHCP server, serves no
// device of the VRF, or is "" when its sockets are bound to the VRF's device: systemd binds them
// to the index the device had when the service started, which the service records in the file
// record, and a VRF made anew has a new one. chalkos-resolvers and chalkos-dhcp restart such a
// service, after a delivery that made the VRF anew.
func (r *side) staleDevice(vrf, record string) string {
	index := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	bound := index(record)
	device := index(filepath.Join(cmp.Or(r.NetDir, "/sys/class/net"), vrf, "ifindex"))
	switch {
	case bound == "":
		return "it recorded no device"
	case device == "":
		return "the VRF has no device"
	case bound != device:
		return "it is bound to a removed device of the VRF"
	}
	return ""
}

// resolverUnit is the unit of the resolver of the VRF vrf, a name that matched router.ShortName,
// whose dashes systemd-escape escapes.
func resolverUnit(vrf string) string {
	return "unbound@" + strings.ReplaceAll(vrf, "-", `\x2d`) + ".service"
}

// keaServerFile is a rendered config of the output kea: its server's VRF and family, dhcp4 or
// dhcp6, and its unit.
type keaServerFile struct {
	path, vrf, family, unit string
}

// keaServers are the servers whose configs the output kea rendered into dir, the default VRF's
// first, then by VRF, DHCPv4's before DHCPv6's; a file of no VRF's name is left out.
func keaServers(dir string) []keaServerFile {
	var out []keaServerFile
	for _, family := range []string{"dhcp4", "dhcp6"} {
		files, _ := filepath.Glob(filepath.Join(dir, "kea-"+family+"*.json"))
		for _, f := range files {
			vrf := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(f), ".json"), "kea-"+family)
			unit := "kea-" + family + ".service"
			switch {
			case vrf == "":
				vrf = routing.DefaultVRF
			case strings.HasPrefix(vrf, "-") && routing.ShortName.MatchString(vrf[1:]):
				// Only a VRF's name reaches systemctl's command line.
				vrf = vrf[1:]
				unit = "kea-" + family + "@" + strings.ReplaceAll(vrf, "-", `\x2d`) + ".service"
			default:
				continue
			}
			out = append(out, keaServerFile{path: f, vrf: vrf, family: family, unit: unit})
		}
	}
	key := func(f keaServerFile) string {
		if f.vrf == routing.DefaultVRF {
			return "0 " + f.family
		}
		return "1 " + f.vrf + " " + f.family
	}
	slices.SortFunc(out, func(a, b keaServerFile) int { return cmp.Compare(key(a), key(b)) })
	return out
}

// dhcpServers are the state of each DHCP server whose config the output kea rendered, the default
// VRF's first, then by VRF, DHCPv4's before DHCPv6's, all asked within one timeout: whether its
// unit is active, bound to its VRF's device, and its subnets' leases, which Kea counts.
func (r *side) dhcpServers(ctx context.Context) []*routerv1.DHCPServerStatus {
	runDir := filepath.Join(cmp.Or(r.RunDir, "/run"), "kea")
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var servers []*routerv1.DHCPServerStatus
	for _, f := range keaServers(cmp.Or(r.KeaDir, filepath.Join(outputs.DefaultRoot, "kea"))) {
		family := "ipv4"
		if f.family == "dhcp6" {
			family = "ipv6"
		}
		s := &routerv1.DHCPServerStatus{Vrf: f.vrf, Family: family}
		servers = append(servers, s)
		state, err := r.isActive(ctx, budget, f.unit)
		switch {
		case err != nil && state != "":
			s.Error = f.unit + " is " + state
			continue
		case err != nil:
			s.Error = err.Error()
			continue
		}
		if f.vrf != routing.DefaultVRF {
			if stale := r.staleDevice(f.vrf, filepath.Join(runDir, f.family+"-"+f.vrf+".ifindex")); stale != "" {
				s.Error = f.unit + " is active, but " + stale
				continue
			}
		}
		data, err := os.ReadFile(f.path)
		var subnets []routing.KeaSubnet
		if err == nil {
			subnets, err = routing.ParseKeaSubnets(data)
		}
		if err != nil {
			s.Error = err.Error()
			continue
		}
		args, err := routing.KeaAsk(budget, routing.KeaSocket(runDir, f.family, f.vrf), "statistic-get-all")
		var leases map[uint32]routing.KeaLeases
		if err == nil {
			leases, err = routing.ParseKeaStatistics(args)
		}
		if msg := keaFailure(ctx, budget, err); msg != "" {
			s.Error = msg
			continue
		}
		s.Running = true
		for _, sub := range subnets {
			l := leases[sub.ID]
			s.Subnets = append(s.Subnets, &routerv1.DHCPSubnetStatus{Name: sub.Name, Subnet: sub.Subnet, Leased: l.Assigned, Pool: l.Total, Delegated: l.Delegated, Delegable: l.Delegable})
		}
	}
	return servers
}

// dhcpRelays are the state of the DHCP relay of each VRF whose config the output dhcp-relay
// rendered, the default VRF's first, then by VRF: whether its unit is active and bound to its
// VRF's device.
func (r *side) dhcpRelays(ctx context.Context) []*routerv1.DHCPRelayStatus {
	dir := cmp.Or(r.RelayDir, filepath.Join(outputs.DefaultRoot, "dhcp-relay"))
	files, _ := filepath.Glob(filepath.Join(dir, "dnsmasq-relay-*.conf"))
	if _, err := os.Stat(filepath.Join(dir, "dnsmasq-relay.conf")); err == nil {
		files = append([]string{filepath.Join(dir, "dnsmasq-relay.conf")}, files...)
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var relays []*routerv1.DHCPRelayStatus
	for _, f := range files {
		vrf, unit := routing.DefaultVRF, "dnsmasq-relay.service"
		if base := filepath.Base(f); base != "dnsmasq-relay.conf" {
			vrf = strings.TrimSuffix(strings.TrimPrefix(base, "dnsmasq-relay-"), ".conf")
			// Only a VRF's name reaches systemctl's command line.
			if !routing.ShortName.MatchString(vrf) {
				continue
			}
			unit = "dnsmasq-relay@" + strings.ReplaceAll(vrf, "-", `\x2d`) + ".service"
		}
		s := &routerv1.DHCPRelayStatus{Vrf: vrf}
		relays = append(relays, s)
		state, err := r.isActive(ctx, budget, unit)
		switch {
		case err != nil && state != "":
			s.Error = unit + " is " + state
		case err != nil:
			s.Error = err.Error()
		case vrf != routing.DefaultVRF:
			if stale := r.staleDevice(vrf, filepath.Join(cmp.Or(r.RunDir, "/run"), "dnsmasq-relay", vrf+".ifindex")); stale != "" {
				s.Error = unit + " is active, but " + stale
				continue
			}
			s.Running = true
		default:
			s.Running = true
		}
	}
	return relays
}

func (p *Plugin) Routes(ctx context.Context, req *connect.Request[routerv1.RoutesRequest]) (*connect.Response[routerv1.RoutesResponse], error) {
	// The request's strings reach no command: the filter parses them, a VRF's name passes its
	// pattern and names a running VRF document, and vtysh is asked for whole families alone.
	table, vrf := req.Msg.Table, req.Msg.Vrf
	if vrf != "" && table != "" {
		return nil, failed(connect.CodeInvalidArgument, "give a VRF or a table, not both: a VRF's routes are those of its table")
	}
	named := vrf != "" && vrf != routing.DefaultVRF
	if named {
		t, err := p.r.vrfTable(vrf)
		if err != nil {
			return nil, err
		}
		table = strconv.FormatUint(uint64(t), 10)
	}
	f, err := routing.NewFilter(req.Msg.Prefix, req.Msg.Protocol, table)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	list := p.r.KernelRoutes
	if list == nil {
		list = routing.KernelRoutes
	}
	kernel, err := list()
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	resp := &routerv1.RoutesResponse{Vrf: cmp.Or(vrf, routing.DefaultVRF)}
	for _, r := range f.Kernel(kernel) {
		resp.Kernel = append(resp.Kernel, kernelRoute(r))
	}
	if f.WantsBGP() && p.r.hasBGP() {
		for _, family := range []string{"ipv4", "ipv6"} {
			command := fmt.Sprintf(routing.RoutesCommand, family)
			if named {
				command = fmt.Sprintf(routing.VRFRoutesCommand, vrf, family)
			}
			out, err := p.r.vtysh(ctx, command)
			var routes []*routing.BGPRoute
			if err == nil {
				routes, err = routing.ParseRoutes(out)
			}
			if err != nil {
				resp.Bgp, resp.BgpError = nil, err.Error()
				break
			}
			for _, r := range f.BGP(routes) {
				resp.Bgp = append(resp.Bgp, bgpRoute(r))
			}
		}
	}
	return connect.NewResponse(resp), nil
}

// defaultVRFDir holds the running VRF documents, as chalkd writes the node's config for its
// services.
const defaultVRFDir = "/run/chalkos/config/router.chalkos.dev/VRF"

// vrfTable is the table of the node's VRF of the name, as its running VRF document gives it.
func (r *side) vrfTable(name string) (uint32, error) {
	if !routing.ShortName.MatchString(name) {
		return 0, failed(connect.CodeInvalidArgument, "vrf %q is no VRF document's name", name)
	}
	data, err := os.ReadFile(filepath.Join(cmp.Or(r.VRFDir, defaultVRFDir), name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, failed(connect.CodeNotFound, "the node has no VRF %s", name)
	}
	if err != nil {
		return 0, failed(connect.CodeInternal, "read the VRF %s: %v", name, err)
	}
	var doc struct {
		Spec struct {
			Table uint32 `json:"table"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, failed(connect.CodeInternal, "read the VRF %s: %v", name, err)
	}
	if doc.Spec.Table == 0 {
		return 0, failed(connect.CodeInternal, "read the VRF %s: its document holds no table", name)
	}
	return doc.Spec.Table, nil
}

// peerStatus is a BGP session as the router's status reports it.
func peerStatus(p *routing.Peer) *routerv1.BGPPeerStatus {
	return &routerv1.BGPPeerStatus{Vrf: p.VRF, Name: p.Name, Neighbor: p.Neighbor, State: p.State, UptimeSeconds: p.UptimeSeconds,
		PrefixesReceived: p.PrefixesReceived, PrefixesSent: p.PrefixesSent, Bfd: p.BFD}
}

// kernelRoute is a kernel route as Routes answers it.
func kernelRoute(r *routing.KernelRoute) *routerv1.KernelRoute {
	k := &routerv1.KernelRoute{Destination: r.Destination, Type: r.Type, Protocol: r.Protocol, Table: r.Table, Metric: r.Metric}
	for _, h := range r.NextHops {
		k.NextHops = append(k.NextHops, &routerv1.NextHop{Gateway: h.Gateway, Interface: h.Interface, Weight: h.Weight})
	}
	return k
}

// bgpRoute is a BGP path as Routes answers it.
func bgpRoute(r *routing.BGPRoute) *routerv1.BGPRoute {
	return &routerv1.BGPRoute{Prefix: r.Prefix, AsPath: r.ASPath, NextHops: r.NextHops, Peer: r.Peer, LocalPref: r.LocalPref,
		Med: r.MED, Communities: r.Communities, Best: r.Best, ImportedFrom: r.ImportedFrom}
}

func failed(code connect.Code, format string, args ...any) error {
	return connect.NewError(code, fmt.Errorf(format, args...))
}

// keaFailure is the error of a server's status whose question to Kea ended with err, while the
// request ctx and the questions' budget ran, or empty when Kea answered: an answer counts even if
// the budget ran out right after it.
func keaFailure(ctx, budget context.Context, err error) string {
	switch {
	case err == nil:
		return ""
	case ctx.Err() != nil:
		return ctx.Err().Error()
	// The socket's deadline, the budget's, may pass a moment before the budget ends.
	case errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(budget.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("Kea did not answer within %v", askTimeout)
	default:
		return err.Error()
	}
}
