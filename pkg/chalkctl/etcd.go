package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/manifest"
)

// throughControlPlane calls call on the control-plane node via, or else on the cluster's
// control-plane nodes but skip in name order, moving on past nodes that are down or no etcd member.
func (a *app) throughControlPlane(ctx context.Context, n nodeCommand, via, skip string, call func(conn *client.Conn) error) error {
	creds, err := a.loadCredentials(ctx, n.secrets, n.config, n.cluster.flake)
	if err != nil {
		return err
	}
	c, err := a.clusterFor(ctx, n.cluster, creds)
	if err != nil {
		return err
	}
	names := []string{via}
	if via == "" {
		if n.endpoint != "" {
			return errors.New("--endpoint reaches one node; name it with --via")
		}
		if c.partial {
			return errors.New("a client file does not say which nodes are control planes; name one with --via, or pass --flake or --manifest")
		}
		names = controlPlaneNodes(c.manifest, skip)
		if len(names) == 0 {
			return errors.New("the cluster has no other control-plane node to ask")
		}
	}
	var last error
	for _, name := range names {
		t, err := targetIn(c, n, name, creds)
		if err != nil {
			return err
		}
		conn, err := dialInstalled(t)
		if err != nil {
			return err
		}
		err = call(conn)
		if err == nil {
			return nil
		}
		if via != "" || !tryNextControlPlane(err) {
			return fmt.Errorf("%s: %w", name, err)
		}
		last = fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("no control-plane node answered: %w", last)
}

// tryNextControlPlane reports whether another control plane may answer where one failed with
// err: one that is down, or one that is no etcd member, as before it joined.
func tryNextControlPlane(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return true
	case connect.CodeFailedPrecondition:
		var ce *connect.Error
		return errors.As(err, &ce) && strings.Contains(ce.Message(), "is not an etcd member")
	}
	return false
}

// controlPlaneNodes returns the cluster's control-plane nodes but skip, in name order.
func controlPlaneNodes(m *manifest.Manifest, skip string) []string {
	var names []string
	for name, node := range m.Nodes {
		if name != skip && m.Roles[node.Role].Kind == manifest.KindControlPlane {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// etcdFlags are the flags of the etcd commands.
type etcdFlags struct {
	node  nodeCommand
	via   string
	force bool
}

func (a *app) etcdMembersCommand() *cobra.Command {
	var f etcdFlags
	cmd := a.command(&cobra.Command{
		Use:   "members",
		Short: "List etcd's members and their health",
	}, func(a *app, ctx context.Context, pos []string) error { return a.etcdMembers(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	cmd.Flags().StringVar(&f.via, "via", "", "control-plane node to ask (default the first one that answers)")
	return cmd
}

func (a *app) etcdMembers(ctx context.Context, f etcdFlags, pos []string) error {
	if len(pos) != 0 {
		return errors.New("usage: chalkctl etcd members [--via NODE]")
	}
	var members []*nodev1.EtcdMember
	err := a.throughControlPlane(ctx, f.node, f.via, "", func(conn *client.Conn) error {
		resp, err := conn.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tPEER URLS\tMEMBER\tHEALTH")
	for _, m := range members {
		name, kind, health := m.Name, "voter", "healthy"
		if name == "" {
			name = "(not started)"
		}
		if m.Learner {
			kind = "learner"
		}
		if m.Unhealthy != "" {
			health = "unhealthy: " + m.Unhealthy
		}
		fmt.Fprintf(w, "%s\t%x\t%s\t%s\t%s\n", name, m.Id, strings.Join(m.PeerUrls, ","), kind, health)
	}
	return w.Flush()
}

func (a *app) etcdRemoveMemberCommand() *cobra.Command {
	var f etcdFlags
	cmd := a.command(&cobra.Command{
		Use:   "remove-member <node|id>",
		Short: "Remove a node's etcd member, such as a stale one",
	}, func(a *app, ctx context.Context, pos []string) error { return a.etcdRemoveMember(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	fs.StringVar(&f.via, "via", "", "control-plane node that removes the member (default the first other one that answers)")
	fs.BoolVar(&f.force, "force", false, "remove the member even when the voters left would have no healthy quorum")
	return cmd
}

func (a *app) etcdRemoveMember(ctx context.Context, f etcdFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl etcd remove-member <node|id> [--via NODE] [--force]")
	}
	var removed *nodev1.EtcdMember
	err := a.throughControlPlane(ctx, f.node, f.via, pos[0], func(conn *client.Conn) error {
		resp, err := conn.EtcdRemoveMember(ctx, connect.NewRequest(&nodev1.EtcdRemoveMemberRequest{Member: pos[0], Force: f.force}))
		if err == nil {
			removed = resp.Msg.Removed
		}
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "removed the etcd member %s (%x)\n", pos[0], removed.Id)
	return nil
}

func (a *app) etcdLeaveCommand() *cobra.Command {
	var f etcdFlags
	cmd := a.command(&cobra.Command{
		Use:   "leave <node>",
		Short: "Take a control-plane node out of etcd",
	}, func(a *app, ctx context.Context, pos []string) error { return a.etcdLeave(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	cmd.Flags().BoolVar(&f.force, "force", false, "leave through the other members also when the node's own etcd member does not answer, as when it lost its pinned address")
	return cmd
}

func (a *app) etcdLeave(ctx context.Context, f etcdFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl etcd leave <node> [--force]")
	}
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	if _, err := conn.EtcdLeave(ctx, connect.NewRequest(&nodev1.EtcdLeaveRequest{Force: f.force})); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	fmt.Fprintf(a.stdout, "%s left etcd; reinstall it to join the cluster again\n", t.name)
	return nil
}
