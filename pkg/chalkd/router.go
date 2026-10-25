package chalkd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool"
)

// Router is the node's router side: the state of its FRR and the kernel's routes.
type Router struct {
	// Run runs vtysh, which FRR answers on its sockets in /run/frr.
	Run tool.Runner
	// KernelRoutes lists the kernel's routes; nil reads them through netlink. Tests replace it.
	KernelRoutes func() ([]*nodev1.KernelRoute, error)
}

// vtyshTimeout bounds each question chalkd asks FRR, so Status answers while FRR hangs. Tests
// shorten it.
var vtyshTimeout = 5 * time.Second

// vtysh runs one of the fixed show commands of package router, whose standard error, while FRR is
// down, goes into the error alone rather than into chalkd's log as well.
func (r *Router) vtysh(ctx context.Context, command string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, vtyshTimeout)
	defer cancel()
	run := r.Run.Run
	if quiet, ok := r.Run.(tool.QuietRunner); ok {
		run = quiet.RunQuiet
	}
	out, err := run(runCtx, "vtysh", "-c", command)
	if err != nil {
		// The caller's own end is not vtysh's timeout.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("vtysh did not answer within %v", vtyshTimeout)
		}
	}
	return out, err
}

// status is the router's part of the node's status: its BGP sessions, or why FRR did not say.
func (r *Router) status(ctx context.Context) *nodev1.RouterStatus {
	out, err := r.vtysh(ctx, router.NeighborsCommand)
	if err != nil {
		return &nodev1.RouterStatus{BgpError: err.Error()}
	}
	peers, err := router.ParsePeers(out)
	if err != nil {
		return &nodev1.RouterStatus{BgpError: err.Error()}
	}
	return &nodev1.RouterStatus{Peers: peers}
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
