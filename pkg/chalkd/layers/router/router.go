// Package router is chalkd's router layer: it serves RouterService with the state of the node's
// FRR, Jool and Unbound and the kernel's routes.
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
)

// Options set up the router layer; zero fields mean chalkd's own.
type Options struct {
	// KernelRoutes lists the kernel's routes; nil reads them through netlink.
	KernelRoutes func() ([]*routing.KernelRoute, error)
	// ResolverConfig is Unbound's config, which exists on a node with a Resolver document;
	// empty is /etc/unbound/unbound.conf.
	ResolverConfig string
	// NAT64Dir is the output jool's directory, with a file <name>.json per NAT64 document; empty
	// is /run/chalkos/outputs/jool.
	NAT64Dir string
	// VRFDir holds the node's running VRF documents, <name>.json each; empty is
	// /run/chalkos/config/router.chalkos.dev/VRF.
	VRFDir string
}

// side is the node's router side: the state of its FRR, Jool and Unbound and the kernel's routes.
type side struct {
	// run runs vtysh, which FRR answers on its sockets in /run/frr, nsenter and jool, and
	// unbound-control.
	run tool.Runner
	Options
}

// Plugin is chalkd's router layer: it serves RouterService for the node's router side.
type Plugin struct{ r *side }

var _ layer.Plugin = (*Plugin)(nil)

// New is the router layer of the node, which runs vtysh, nsenter and jool, and unbound-control
// through the node's runner.
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
	run := r.run.Run
	if quiet, ok := r.run.(tool.QuietRunner); ok {
		run = quiet.RunQuiet
	}
	out, err := run(budget, program, args...)
	if err != nil {
		// The caller's own end is not the daemon's timeout.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(budget.Err(), context.DeadlineExceeded) {
			// nsenter runs the program that did not answer.
			who := program
			if program == "nsenter" && len(args) > 1 {
				who = args[1]
			}
			return nil, fmt.Errorf("%s did not answer within %v", who, askTimeout)
		}
	}
	return out, err
}

// vtysh runs one of the show commands of package router, which may carry a VRF's name that
// matched router.ShortName.
func (r *side) vtysh(ctx context.Context, command string) ([]byte, error) {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	return r.ask(ctx, budget, "vtysh", "-c", command)
}

// status is the state of the router: its BGP sessions, its NAT64 instances and its resolver, or
// why a daemon did not say. The daemons are asked side by side, so daemons that hang cost one
// timeout altogether.
func (r *side) status(ctx context.Context) *routerv1.StatusResponse {
	st := &routerv1.StatusResponse{}
	var wg sync.WaitGroup
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
	wg.Go(func() {
		var err error
		if st.Nat64, err = r.nat64(ctx); err != nil {
			st.Nat64, st.Nat64Error = nil, err.Error()
		}
	})
	wg.Go(func() { st.Resolver = r.resolver(ctx) })
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
		if te := (*tool.Error)(nil); errors.As(err, &te) && (strings.Contains(te.Stderr, routing.JoolMissingInstance) || strings.Contains(te.Stderr, routing.NamespaceMissing)) {
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
	if f.WantsBGP() {
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
