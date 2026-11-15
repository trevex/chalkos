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
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/services"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// Options set up the router layer; zero fields mean chalkd's own. The output directories hold
// one config per VRF, as package services names them.
type Options struct {
	// KernelRoutes lists the kernel's routes; nil reads them through netlink.
	KernelRoutes func() ([]*routing.KernelRoute, error)
	// FRRConfig is the link to the config frr.service fills in, which images with BGP carry
	// whether FRR runs or not; chalkd asks FRR nothing without it. Default /etc/frr/frr.conf.
	FRRConfig string
	// ResolverConfig is Unbound's config, present with a Resolver document; default
	// /etc/unbound/unbound.conf.
	ResolverConfig string
	// ResolverDir, KeaDir, RelayDir and NAT64Dir are the directories of the outputs unbound, kea,
	// dhcp-relay and jool (a <name>.json per NAT64 document), below /run/chalkos/outputs by
	// default.
	ResolverDir, KeaDir, RelayDir, NAT64Dir string
	// PDDir holds the records of the prefixes Kea's DHCPv6 servers delegated; default
	// /var/lib/chalkos-dhcp6-pd.
	PDDir string
	// VRFDir holds the running VRF documents, <name>.json each; default
	// /run/chalkos/config/router.chalkos.dev/VRF.
	VRFDir string
	// RunDir holds the per-VRF services' runtime directories, with Kea's control sockets, where each
	// records the index of the device its sockets are bound to; default /run.
	RunDir string
	// NetDir holds a directory per network device, with its index; default /sys/class/net.
	NetDir string
}

// withDefaults returns o with each zero field set to chalkd's own.
func (o Options) withDefaults() Options {
	if o.KernelRoutes == nil {
		o.KernelRoutes = routing.KernelRoutes
	}
	o.FRRConfig = cmp.Or(o.FRRConfig, "/etc/frr/frr.conf")
	o.ResolverConfig = cmp.Or(o.ResolverConfig, "/etc/unbound/unbound.conf")
	o.ResolverDir = cmp.Or(o.ResolverDir, filepath.Join(outputs.DefaultRoot, "unbound"))
	o.KeaDir = cmp.Or(o.KeaDir, filepath.Join(outputs.DefaultRoot, "kea"))
	o.PDDir = cmp.Or(o.PDDir, routing.PDRecordDir)
	o.RelayDir = cmp.Or(o.RelayDir, filepath.Join(outputs.DefaultRoot, "dhcp-relay"))
	o.NAT64Dir = cmp.Or(o.NAT64Dir, filepath.Join(outputs.DefaultRoot, "jool"))
	o.VRFDir = cmp.Or(o.VRFDir, defaultVRFDir)
	o.RunDir = cmp.Or(o.RunDir, "/run")
	o.NetDir = cmp.Or(o.NetDir, "/sys/class/net")
	return o
}

// Plugin is chalkd's router layer: it serves RouterService with the state of the node's FRR,
// Jool, Unbound and Kea and the kernel's routes.
type Plugin struct {
	// run runs vtysh, which FRR answers on its sockets in /run/frr, nsenter and jool,
	// unbound-control and systemctl.
	run tool.Runner
	Options
}

var _ layer.Plugin = (*Plugin)(nil)

// New is the router layer of the node, which runs vtysh, nsenter and jool, and unbound-control
// through the node's runner, and asks Kea on its control sockets.
func New(node layer.Node, o Options) *Plugin {
	return &Plugin{run: node.Runner(), Options: o.withDefaults()}
}

func (p *Plugin) Name() string { return layers.Router }

func (p *Plugin) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return routerv1connect.NewRouterServiceHandler(p, opts...)
}

func (p *Plugin) Permissions() map[string]layer.Permission {
	return map[string]layer.Permission{
		routerv1connect.RouterServiceStatusProcedure: {Modes: layer.NormalOnly, Role: pki.RoleReader, Console: layer.ConsoleAllowed},
		routerv1connect.RouterServiceRoutesProcedure: {Modes: layer.NormalOnly, Role: pki.RoleReader},
	}
}

// Start runs nothing: the router's state is read when it is asked for.
func (p *Plugin) Start(context.Context) error { return nil }

func (p *Plugin) Status(ctx context.Context, _ *connect.Request[routerv1.StatusRequest]) (*connect.Response[routerv1.StatusResponse], error) {
	return connect.NewResponse(p.status(ctx)), nil
}

// askTimeout bounds the questions chalkd asks one router daemon, so Status answers while one
// hangs. Tests shorten it.
var askTimeout = 5 * time.Second

// ask runs one of the router's fixed questions to its daemons, whose standard error, while the
// daemon is down, goes into the error alone rather than into chalkd's log as well. budget is ctx
// bounded by askTimeout, which the questions to one daemon share.
func (p *Plugin) ask(ctx, budget context.Context, program string, args ...string) ([]byte, error) {
	out, err := p.run.Exec(budget, tool.Cmd{Name: program, Args: args, Quiet: true})
	if err != nil {
		// nsenter runs the program that did not answer.
		who := program
		if program == "nsenter" && len(args) > 1 {
			who = args[1]
		}
		return nil, ended(ctx, budget, who, err)
	}
	return out, nil
}

// ended is why a question to who failed with err: the caller's own end, which is no timeout of
// who's, who's timeout once budget ran out, or else err. It is nil for a nil err, so an answer
// counts even when budget ends right after it.
func ended(ctx, budget context.Context, who string, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	// A socket's deadline, the budget's, may pass a moment before the budget ends.
	case errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(budget.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s did not answer within %v", who, askTimeout)
	}
	return err
}

// hasBGP reports whether the image runs BGP: whether it carries FRR's config link, which counts
// while FRR has not filled the config in. An error other than the link's absence counts as BGP,
// whose questions then say what is wrong.
func (p *Plugin) hasBGP() bool {
	_, err := os.Lstat(p.FRRConfig)
	return !errors.Is(err, fs.ErrNotExist)
}

// vtysh runs one of the show commands of package router, which may carry a VRF's name that
// matched router.ShortName.
func (p *Plugin) vtysh(ctx context.Context, command string) ([]byte, error) {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	return p.ask(ctx, budget, "vtysh", "-c", command)
}

// status is the state of the router: its BGP sessions, its NAT64 instances and its resolver, or
// why a daemon did not say; an image without BGP asks FRR nothing. The daemons are asked side by
// side, so daemons that hang cost one timeout altogether.
func (p *Plugin) status(ctx context.Context) *routerv1.StatusResponse {
	st := &routerv1.StatusResponse{BgpAbsent: !p.hasBGP()}
	var wg sync.WaitGroup
	if !st.BgpAbsent {
		wg.Go(func() {
			out, err := p.vtysh(ctx, routing.NeighborsCommand)
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
		if st.Nat64, err = p.nat64(ctx); err != nil {
			st.Nat64, st.Nat64Error = nil, err.Error()
		}
	})
	wg.Go(func() { st.Resolver = p.resolver(ctx) })
	wg.Go(func() { st.VrfResolvers = p.vrfResolvers(ctx) })
	wg.Go(func() { st.Dhcp = p.dhcpServers(ctx) })
	wg.Go(func() { st.DhcpRelays = p.dhcpRelays(ctx) })
	wg.Wait()
	return st
}

// nat64 is the state of the Jool instances of the node's NAT64 documents, all asked within one
// timeout; an instance removed meanwhile is left out.
func (p *Plugin) nat64(ctx context.Context) ([]*routerv1.NAT64Status, error) {
	rendered, err := p.renderedNAT64()
	if err != nil || len(rendered) == 0 {
		return nil, err
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	out, err := p.ask(ctx, budget, "jool", routing.JoolInstancesArgs...)
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
		n, err := p.nat64Instance(ctx, budget, name)
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
func (p *Plugin) nat64Instance(ctx, budget context.Context, name string) (*routerv1.NAT64Status, error) {
	// Each instance runs in a network namespace of its own, and jool asks the namespace it runs in.
	jool := func(args []string) ([]byte, error) {
		out, err := p.ask(ctx, budget, "nsenter", routing.InNamespace(name, args)...)
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
func (p *Plugin) renderedNAT64() ([]string, error) {
	entries, err := os.ReadDir(p.NAT64Dir)
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
func (p *Plugin) resolver(ctx context.Context) *routerv1.ResolverStatus {
	if _, err := os.Stat(p.ResolverConfig); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	if _, err := p.ask(ctx, budget, "unbound-control", "status"); err != nil {
		return &routerv1.ResolverStatus{Error: err.Error()}
	}
	return &routerv1.ResolverStatus{Running: true}
}

// renderedVRFs are the VRFs of the configs of the service svc that an output rendered into dir,
// the default VRF first, then by name. A file whose name does not match routing.ShortName is left
// out, so only a VRF's name reaches a command line: every VRF's name matches it, as the kind VRF
// refuses names over 15 characters.
func renderedVRFs(dir string, svc services.Service) []string {
	entries, _ := os.ReadDir(dir)
	var vrfs []string
	for _, e := range entries {
		if vrf, ok := svc.VRFOfFile(e.Name()); ok && routing.ShortName.MatchString(vrf) {
			vrfs = append(vrfs, vrf)
		}
	}
	slices.SortFunc(vrfs, derive.CompareVRFs)
	return vrfs
}

// unitRunning reports whether the unit of the instance of the service svc of the VRF vrf is
// active and, for a VRF's, bound to the VRF's device, or says why not, asked within budget as ask
// asks.
func (p *Plugin) unitRunning(ctx, budget context.Context, svc services.Service, vrf string) (running bool, problem string) {
	unit := svc.Unit(vrf)
	state, err := systemctl.IsActive(budget, p.run, unit)
	if why := ended(ctx, budget, "systemctl", err); why != err {
		// The question timed out or the caller ended: no state systemctl gave counts.
		state, err = "", why
	}
	switch {
	case err != nil && state != "":
		return false, unit + " is " + state
	case err != nil:
		return false, err.Error()
	}
	if record := svc.IndexRecord(vrf); record != "" {
		if stale := p.staleDevice(vrf, filepath.Join(p.RunDir, record)); stale != "" {
			return false, unit + " is active, but " + stale
		}
	}
	return true, ""
}

// vrfResolvers are the state of the unit unbound@<vrf> of each VRF whose config the output unbound
// rendered, sorted by VRF, all asked within one timeout. An active unit whose sockets are bound to
// no device of its VRF does not count as running.
func (p *Plugin) vrfResolvers(ctx context.Context) []*routerv1.ResolverStatus {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var resolvers []*routerv1.ResolverStatus
	// The instances are the VRFs' alone: resolver asks the default VRF's.
	for _, in := range p.instances("unbound") {
		s := &routerv1.ResolverStatus{Vrf: in.vrf}
		s.Running, s.Error = p.unitRunning(ctx, budget, in.svc, in.vrf)
		resolvers = append(resolvers, s)
	}
	return resolvers
}

// instances are the instances of the per-VRF service of the name whose configs its output
// rendered, the default VRF's first, then by VRF, and by service within a VRF.
func (p *Plugin) instances(name string) []instance {
	want := wanted(p.perVRFServices()[name])
	slices.SortStableFunc(want, func(a, b instance) int { return derive.CompareVRFs(a.vrf, b.vrf) })
	return want
}

// staleDevice says why the VRF service whose device index record holds no longer serves the VRF's
// device, or "": systemd binds its sockets to the ifindex at start, and a re-created VRF gets a
// new one, which chalkd router services restarts the service on.
func (p *Plugin) staleDevice(vrf, record string) string {
	index := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	bound := index(record)
	device := index(filepath.Join(p.NetDir, vrf, "ifindex"))
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

// dhcpServers are the state of each DHCP server whose config the output kea rendered, the default
// VRF's first, then by VRF, DHCPv4's before DHCPv6's, all asked within one timeout: whether its
// unit is active, bound to its VRF's device, and its subnets' leases, which Kea counts.
func (p *Plugin) dhcpServers(ctx context.Context) []*routerv1.DHCPServerStatus {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var servers []*routerv1.DHCPServerStatus
	for _, in := range p.instances("kea") {
		s := &routerv1.DHCPServerStatus{Vrf: in.vrf, Family: "ipv4"}
		if in.svc == services.Kea6 {
			s.Family = "ipv6"
		}
		servers = append(servers, s)
		if running, problem := p.unitRunning(ctx, budget, in.svc, in.vrf); !running {
			s.Error = problem
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.KeaDir, in.svc.File(in.vrf)))
		var subnets []routing.KeaSubnet
		if err == nil {
			subnets, err = routing.ParseKeaSubnets(data)
		}
		if err != nil {
			s.Error = err.Error()
			continue
		}
		args, err := routing.KeaAsk(budget, in.svc.KeaSocket(p.RunDir, in.vrf), "statistic-get-all")
		var leases map[uint32]routing.KeaLeases
		if err == nil {
			leases, err = routing.ParseKeaStatistics(args)
		}
		if err := ended(ctx, budget, "Kea", err); err != nil {
			s.Error = err.Error()
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
func (p *Plugin) dhcpRelays(ctx context.Context) []*routerv1.DHCPRelayStatus {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var relays []*routerv1.DHCPRelayStatus
	for _, in := range p.instances("relay") {
		s := &routerv1.DHCPRelayStatus{Vrf: in.vrf}
		s.Running, s.Error = p.unitRunning(ctx, budget, in.svc, in.vrf)
		relays = append(relays, s)
	}
	return relays
}

func (p *Plugin) Routes(ctx context.Context, req *connect.Request[routerv1.RoutesRequest]) (*connect.Response[routerv1.RoutesResponse], error) {
	// The request's strings reach no command: the filter parses them, a VRF's name passes its
	// pattern and names a running VRF document, and vtysh is asked for whole families alone.
	table, vrf := req.Msg.Table, req.Msg.Vrf
	if vrf != "" && table != "" {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "give a VRF or a table, not both: a VRF's routes are those of its table")
	}
	named := vrf != "" && vrf != derive.DefaultVRF
	if named {
		t, err := p.vrfTable(vrf)
		if err != nil {
			return nil, err
		}
		table = strconv.FormatUint(uint64(t), 10)
	}
	f, err := routing.NewFilter(req.Msg.Prefix, req.Msg.Protocol, table)
	if err != nil {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "%w", err)
	}
	kernel, err := p.KernelRoutes()
	if err != nil {
		return nil, err
	}
	resp := &routerv1.RoutesResponse{Vrf: cmp.Or(vrf, derive.DefaultVRF)}
	for _, r := range f.Kernel(kernel) {
		resp.Kernel = append(resp.Kernel, kernelRoute(r))
	}
	if f.WantsBGP() && p.hasBGP() {
		for _, family := range []string{"ipv4", "ipv6"} {
			command := fmt.Sprintf(routing.RoutesCommand, family)
			if named {
				command = fmt.Sprintf(routing.VRFRoutesCommand, vrf, family)
			}
			out, err := p.vtysh(ctx, command)
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
func (p *Plugin) vrfTable(name string) (uint32, error) {
	if !routing.ShortName.MatchString(name) {
		return 0, layer.Errorf(connect.CodeInvalidArgument, "vrf %q is no VRF document's name", name)
	}
	data, err := os.ReadFile(filepath.Join(p.VRFDir, name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, layer.Errorf(connect.CodeNotFound, "the node has no VRF %s", name)
	}
	if err != nil {
		return 0, fmt.Errorf("read the VRF %s: %w", name, err)
	}
	var doc struct {
		Spec struct {
			Table uint32 `json:"table"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("read the VRF %s: %w", name, err)
	}
	if doc.Spec.Table == 0 {
		return 0, fmt.Errorf("read the VRF %s: its document holds no table", name)
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
