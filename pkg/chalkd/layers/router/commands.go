package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/outputs"
	routing "github.com/trevex/chalkos/pkg/router"
)

// Commands are the router layer's subcommands of chalkd.
func (p *Plugin) Commands() map[string]layer.Command {
	return map[string]layer.Command{
		"dhcp6-routes": {
			Help: "route each prefix the DHCPv6 servers delegated to the link-local address\nthat asked for it, until its lease ends, and remove the other routes of\ndelegated prefixes",
			Run: layer.NoArgs(func() error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				return p.r.dhcp6Routes(ctx, time.Now())
			}),
		},
	}
}

// dhcp6Routes routes each delegated prefix that the hook of Kea's DHCPv6 servers recorded, and
// that a rendered config delegates on the record's link, to the address that asked for it, in
// the link's VRF, until its lease ends, and removes the routes of protocol dhcp-pd of no such
// record. It removes no record: the hook removes those of released and expired leases, and
// ended records route nothing. With more records than it reads, it removes no route, and
// without a rendered DHCPv6 config, as in maintenance mode, it changes nothing: the kernel
// removes the routes when their leases end. A route ip does not add or remove is named on
// standard error and left to the next run, as the next record or renewal starts one: a unit that
// failed for a link that is down at boot would fail the boot's health check.
func (r *side) dhcp6Routes(ctx context.Context, now time.Time) error {
	dir := cmp.Or(r.KeaDir, filepath.Join(outputs.DefaultRoot, "kea"))
	var pools []routing.PDPool
	files, _ := filepath.Glob(filepath.Join(dir, "kea-dhcp6*.json"))
	// Without the output kea, as in maintenance mode, or without a server of DHCPv6, the run
	// changes nothing: the routes end with their leases.
	if len(files) == 0 {
		return nil
	}
	for _, f := range files {
		vrf := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(f), ".json"), "kea-dhcp6")
		vrf = cmp.Or(strings.TrimPrefix(vrf, "-"), routing.DefaultVRF)
		// Only a VRF's name reaches ip's command line.
		if vrf != routing.DefaultVRF && !routing.ShortName.MatchString(vrf) {
			continue
		}
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		p, err := routing.ParseKea6Pools(vrf, data)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		pools = append(pools, p...)
	}
	routes, err := routing.ReadPDRecords(cmp.Or(r.PDDir, routing.PDRecordDir), pools, now)
	truncated := errors.Is(err, routing.ErrTooManyPDRecords)
	if truncated {
		fmt.Fprintf(os.Stderr, "chalkd router dhcp6-routes: %v; routing the first %d and removing no route\n", err, routing.MaxPDRecords)
	} else if err != nil {
		return err
	}
	list := r.KernelRoutes
	if list == nil {
		list = routing.KernelRoutes
	}
	kernel, err := list()
	if err != nil {
		return err
	}
	for _, k := range kernel {
		if k.Protocol != "dhcp-pd" || len(k.NextHops) != 1 {
			continue
		}
		hop := k.NextHops[0]
		kept := truncated
		for _, rt := range routes {
			kept = kept || (rt.Prefix.String() == k.Destination && rt.Via.String() == hop.Gateway && rt.Link == hop.Interface)
		}
		if kept {
			continue
		}
		if _, err := r.run.Run(ctx, "ip", "-6", "route", "del", k.Destination, "table", strconv.FormatUint(uint64(k.Table), 10), "proto", strconv.Itoa(routing.PDProtocol)); err != nil {
			fmt.Fprintf(os.Stderr, "chalkd router dhcp6-routes: remove the route of %s: %v\n", k.Destination, err)
		}
	}
	for _, rt := range routes {
		args := []string{"-6", "route", "replace", rt.Prefix.String(), "via", rt.Via.String(), "dev", rt.Link, "proto", strconv.Itoa(routing.PDProtocol)}
		if rt.VRF != routing.DefaultVRF {
			args = append(args, "vrf", rt.VRF)
		}
		// The kernel removes the route when the lease ends, should no renewal come.
		args = append(args, "expires", strconv.FormatInt(int64(max(rt.Ends.Sub(now)/time.Second, 1)), 10))
		if _, err := r.run.Run(ctx, "ip", args...); err != nil {
			fmt.Fprintf(os.Stderr, "chalkd router dhcp6-routes: route %s to %s on %s: %v\n", rt.Prefix, rt.Via, rt.Link, err)
		}
	}
	return nil
}
