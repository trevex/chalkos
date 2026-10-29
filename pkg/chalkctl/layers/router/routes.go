package router

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

// Client is the client of the node's router layer over conn.
func Client(conn *client.Conn) routerv1connect.RouterServiceClient {
	return routerv1connect.NewRouterServiceClient(conn.HTTPClient(), conn.BaseURL())
}

type routesFlags struct {
	node            layer.NodeFlags
	protocol, table string
}

func nodeRoutesCommand(h layer.Host) *cobra.Command {
	var f routesFlags
	cmd := h.Command(&cobra.Command{
		Use:     "routes <node> [<prefix>]",
		Short:   "Show a router's routes",
		Long:    nodeRoutesLong,
		Example: nodeRoutesExample,
		Args:    cli.RangeArgs(1, 2),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return nodeRoutes(h, ctx, f, pos) })
	f.node = h.NodeFlags(cmd.Flags())
	cmd.Flags().StringVar(&f.protocol, "protocol", "", "only the kernel's routes of this protocol, such as kernel, static, ra, dhcp or bgp")
	cmd.Flags().StringVar(&f.table, "table", "", "the kernel's routing table: main, local, default, all or a number (default main)")
	return cmd
}

func nodeRoutes(h layer.Host, ctx context.Context, f routesFlags, pos []string) error {
	req := &routerv1.RoutesRequest{Protocol: f.protocol, Table: f.table}
	if len(pos) > 1 {
		req.Prefix = pos[1]
	}
	t, err := h.Target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	conn, err := t.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := Client(conn).Routes(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	r := resp.Msg
	w := h.Table()
	fmt.Fprintln(w, "DESTINATION\tTYPE\tPROTOCOL\tTABLE\tMETRIC\tNEXT HOPS")
	for _, k := range r.Kernel {
		var hops []string
		for _, hop := range k.NextHops {
			hops = append(hops, nextHop(hop))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", k.Destination, k.Type, k.Protocol, k.Table, k.Metric, cmp.Or(strings.Join(hops, ", "), "-"))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	switch {
	case r.BgpError != "":
		fmt.Fprintf(h.Stdout(), "\nbgp: unknown: %s\n", r.BgpError)
	case len(r.Bgp) > 0:
		fmt.Fprintln(h.Stdout())
		w := h.Table()
		fmt.Fprintln(w, "BGP PREFIX\tBEST\tAS PATH\tPEER\tNEXT HOPS\tLOCAL PREF\tMED\tCOMMUNITIES")
		for _, b := range r.Bgp {
			best, pref := "", "-"
			if b.Best {
				best = "*"
			}
			if b.LocalPref != nil {
				pref = fmt.Sprint(*b.LocalPref)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", b.Prefix, best, cmp.Or(b.AsPath, "-"), cmp.Or(b.Peer, "local"),
				cmp.Or(strings.Join(b.NextHops, ", "), "-"), pref, b.Med, cmp.Or(strings.Join(b.Communities, " "), "-"))
		}
		return w.Flush()
	}
	return nil
}

// nextHop is a kernel route's next hop as routes prints it: the gateway on the interface, with
// its weight among several.
func nextHop(h *routerv1.NextHop) string {
	s := h.Interface
	if h.Gateway != "" {
		s = h.Gateway + " on " + h.Interface
	}
	if h.Weight > 0 {
		s += fmt.Sprintf(" (weight %d)", h.Weight)
	}
	return s
}
