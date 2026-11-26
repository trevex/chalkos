package router

import (
	"context"
	"io"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	routerview "github.com/trevex/chalkos/pkg/statusview/layers/router"
)

// ReadStatus reads the router's BGP sessions, its NAT64 instances, its resolvers, its DHCP
// servers and its DHCP relays.
func (p *Plugin) ReadStatus(ctx context.Context, conn *client.Conn) (layer.StatusSection, error) {
	resp, err := Client(conn).Status(ctx, &routerv1.StatusRequest{})
	if err != nil {
		return layer.StatusSection{}, err
	}
	return layer.StatusSection{
		Message:  resp,
		Write:    func(w io.Writer) error { return routerview.Write(w, resp) },
		Problems: routerview.Problems(resp),
	}, nil
}
