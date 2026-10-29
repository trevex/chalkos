package chalkd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
)

// Router is the node's router side: the state of its FRR, Jool and Unbound and the kernel's routes.
type Router struct {
	// Run runs vtysh, which FRR answers on its sockets in /run/frr, jool and unbound-control.
	Run tool.Runner
	// KernelRoutes lists the kernel's routes; nil reads them through netlink. Tests replace it.
	KernelRoutes func() ([]*router.KernelRoute, error)
	// ResolverConfig is Unbound's config, which exists on a node with a Resolver document;
	// empty is /etc/unbound/unbound.conf. Tests replace it.
	ResolverConfig string
	// NAT64Dir is the output jool's directory, with a file <name>.json per NAT64 document; empty
	// is /run/chalkos/outputs/jool. Tests replace it.
	NAT64Dir string
}

// askTimeout bounds the questions chalkd asks one router daemon, so Status answers while one
// hangs. Tests shorten it.
var askTimeout = 5 * time.Second

// ask runs one of the router's fixed questions to its daemons, whose standard error, while the
// daemon is down, goes into the error alone rather than into chalkd's log as well. budget is ctx
// bounded by askTimeout, which the questions to one daemon share.
func (r *Router) ask(ctx, budget context.Context, program string, args ...string) ([]byte, error) {
	run := r.Run.Run
	if quiet, ok := r.Run.(tool.QuietRunner); ok {
		run = quiet.RunQuiet
	}
	out, err := run(budget, program, args...)
	if err != nil {
		// The caller's own end is not the daemon's timeout.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(budget.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s did not answer within %v", program, askTimeout)
		}
	}
	return out, err
}

// vtysh runs one of the fixed show commands of package router.
func (r *Router) vtysh(ctx context.Context, command string) ([]byte, error) {
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	return r.ask(ctx, budget, "vtysh", "-c", command)
}

// status is the router's part of the node's status: its BGP sessions, its NAT64 instances and its
// resolver, or why a daemon did not say. The daemons are asked side by side, so daemons that hang
// cost one timeout altogether.
func (r *Router) status(ctx context.Context) *nodev1.RouterStatus {
	st := &nodev1.RouterStatus{}
	var wg sync.WaitGroup
	wg.Go(func() {
		out, err := r.vtysh(ctx, router.NeighborsCommand)
		var peers []*router.Peer
		if err == nil {
			peers, err = router.ParsePeers(out)
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
func (r *Router) nat64(ctx context.Context) ([]*nodev1.NAT64Status, error) {
	rendered, err := r.renderedNAT64()
	if err != nil || len(rendered) == 0 {
		return nil, err
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	out, err := r.ask(ctx, budget, "jool", router.JoolInstancesArgs...)
	if err != nil {
		return nil, err
	}
	names, err := router.ParseJoolInstances(out)
	if err != nil {
		return nil, err
	}
	var instances []*nodev1.NAT64Status
	for _, name := range names {
		// Jool lists the instances of every network namespace; the node's are those of its NAT64
		// documents.
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
func (r *Router) nat64Instance(ctx, budget context.Context, name string) (*nodev1.NAT64Status, error) {
	jool := func(args []string) ([]byte, error) {
		out, err := r.ask(ctx, budget, "jool", args...)
		if te := (*tool.Error)(nil); errors.As(err, &te) && strings.Contains(te.Stderr, router.JoolMissingInstance) {
			return nil, errInstanceGone
		}
		return out, err
	}
	out, err := jool(router.JoolStatsArgs(name))
	if err != nil {
		return nil, err
	}
	n := &nodev1.NAT64Status{Name: name}
	if n.Sessions, n.Bindings, err = router.ParseJoolStats(out); err != nil {
		return nil, fmt.Errorf("instance %s: %w", name, err)
	}
	for _, protocol := range router.NAT64Protocols {
		out, err := jool(router.JoolPool4Args(name, protocol))
		if err != nil {
			return nil, err
		}
		p := &nodev1.NAT64PoolStatus{Protocol: protocol}
		if p.Size, err = router.ParseJoolPool4(out); err != nil {
			return nil, fmt.Errorf("instance %s: %w", name, err)
		}
		n.Pools = append(n.Pools, p)
	}
	return n, nil
}

// renderedNAT64 are the names of the node's NAT64 documents, whose instances the output jool
// renders as <name>.json.
func (r *Router) renderedNAT64() ([]string, error) {
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
func (r *Router) resolver(ctx context.Context) *nodev1.ResolverStatus {
	if _, err := os.Stat(cmp.Or(r.ResolverConfig, "/etc/unbound/unbound.conf")); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	budget, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	if _, err := r.ask(ctx, budget, "unbound-control", "status"); err != nil {
		return &nodev1.ResolverStatus{Error: err.Error()}
	}
	return &nodev1.ResolverStatus{Running: true}
}

func (s *Server) Routes(ctx context.Context, req *connect.Request[nodev1.RoutesRequest]) (*connect.Response[nodev1.RoutesResponse], error) {
	// The request's strings reach no command: the filter parses them, and vtysh is asked for
	// whole families alone.
	f, err := router.NewFilter(req.Msg.Prefix, req.Msg.Protocol, req.Msg.Table)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	list := s.Router.KernelRoutes
	if list == nil {
		list = router.KernelRoutes
	}
	kernel, err := list()
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	resp := &nodev1.RoutesResponse{}
	for _, r := range f.Kernel(kernel) {
		resp.Kernel = append(resp.Kernel, kernelRoute(r))
	}
	if f.WantsBGP() {
		for _, family := range []string{"ipv4", "ipv6"} {
			out, err := s.Router.vtysh(ctx, fmt.Sprintf(router.RoutesCommand, family))
			var routes []*router.BGPRoute
			if err == nil {
				routes, err = router.ParseRoutes(out)
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

// peerStatus is a BGP session as the router's status reports it.
func peerStatus(p *router.Peer) *nodev1.BGPPeerStatus {
	return &nodev1.BGPPeerStatus{Name: p.Name, Neighbor: p.Neighbor, State: p.State, UptimeSeconds: p.UptimeSeconds,
		PrefixesReceived: p.PrefixesReceived, PrefixesSent: p.PrefixesSent, Bfd: p.BFD}
}

// kernelRoute is a kernel route as Routes answers it.
func kernelRoute(r *router.KernelRoute) *nodev1.KernelRoute {
	k := &nodev1.KernelRoute{Destination: r.Destination, Type: r.Type, Protocol: r.Protocol, Table: r.Table, Metric: r.Metric}
	for _, h := range r.NextHops {
		k.NextHops = append(k.NextHops, &nodev1.NextHop{Gateway: h.Gateway, Interface: h.Interface, Weight: h.Weight})
	}
	return k
}

// bgpRoute is a BGP path as Routes answers it.
func bgpRoute(r *router.BGPRoute) *nodev1.BGPRoute {
	return &nodev1.BGPRoute{Prefix: r.Prefix, AsPath: r.ASPath, NextHops: r.NextHops, Peer: r.Peer, LocalPref: r.LocalPref,
		Med: r.MED, Communities: r.Communities, Best: r.Best}
}
