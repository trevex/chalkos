package router

import (
	"context"

	"connectrpc.com/connect"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	routerview "github.com/trevex/chalkos/pkg/statusview/layers/router"
)

// PrintStatus prints the router's BGP sessions, its NAT64 instances, its resolvers, its DHCP
// servers and its DHCP relays.
func (p *Plugin) PrintStatus(ctx context.Context, h layer.Host, conn *client.Conn) error {
	resp, err := Client(conn).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
	if err != nil {
		return err
	}
	return routerview.Write(h.Stdout(), resp.Msg)
}
