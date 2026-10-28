package chalkd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
)

// Router is the node's router side: the state of its FRR, Jool and Unbound and the kernel's routes.
type Router struct {
	// Run runs vtysh, which FRR answers on its sockets in /run/frr, jool and unbound-control.
	Run tool.Runner
	// KernelRoutes lists the kernel's routes; nil reads them through netlink. Tests replace it.
	KernelRoutes func() ([]*nodev1.KernelRoute, error)
	// ResolverConfig is Unbound's config, which exists on a node with a Resolver document;
	// empty is /etc/unbound/unbound.conf. Tests replace it.
	ResolverConfig string
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
		if err == nil {
			st.Peers, err = router.ParsePeers(out)
		}
		if err != nil {
			st.Peers, st.BgpError = nil, err.Error()
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

// nat64 is the state of Jool's instances: of each protocol, the sessions, the pool's addresses
// and ports they hold, and the pool's size.
func (r *Router) nat64(ctx context.Context) ([]*nodev1.NAT64Status, error) {
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
		n := &nodev1.NAT64Status{Name: name}
		for _, protocol := range router.NAT64Protocols {
			p := &nodev1.NAT64ProtocolStatus{Protocol: protocol}
			for _, t := range []struct {
				table string
				read  func([]byte) (uint64, error)
				into  *uint64
			}{
				{"session", router.CountJoolRows, &p.Sessions},
				{"bib", router.CountJoolRows, &p.Bindings},
				{"pool4", router.ParseJoolPool4, &p.PoolSize},
			} {
				out, err := r.ask(ctx, budget, "jool", router.JoolTableArgs(name, t.table, protocol)...)
				if err != nil {
					return nil, err
				}
				if *t.into, err = t.read(out); err != nil {
					return nil, fmt.Errorf("instance %s: %w", name, err)
				}
			}
			n.Protocols = append(n.Protocols, p)
		}
		instances = append(instances, n)
	}
	return instances, nil
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
	resp := &nodev1.RoutesResponse{Kernel: f.Kernel(kernel)}
	if f.WantsBGP() {
		for _, family := range []string{"ipv4", "ipv6"} {
			out, err := s.Router.vtysh(ctx, fmt.Sprintf(router.RoutesCommand, family))
			var routes []*nodev1.BGPRoute
			if err == nil {
				routes, err = router.ParseRoutes(out)
			}
			if err != nil {
				resp.Bgp, resp.BgpError = nil, err.Error()
				break
			}
			resp.Bgp = append(resp.Bgp, f.BGP(routes)...)
		}
	}
	return connect.NewResponse(resp), nil
}
