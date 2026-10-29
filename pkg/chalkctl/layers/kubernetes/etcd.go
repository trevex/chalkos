package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// throughControlPlane calls call on the control-plane node via, or else on the cluster's
// control-plane nodes but skip in name order, moving on past nodes that are down or no etcd member.
func throughControlPlane(h layer.Host, ctx context.Context, f viaFlags, skip string, call func(conn *client.Conn) error) error {
	nodes, err := h.Nodes(ctx, f.node, f.via)
	if err != nil {
		return err
	}
	if f.via != "" {
		return nodes.ViaControlPlanes([]string{f.via}, func(error) bool { return false }, call)
	}
	if f.node.Address() != "" {
		return errors.New("--address reaches one node; name it with --via")
	}
	c := nodes.Cluster()
	if c.Partial() {
		return errors.New("an inventory names the control planes; name one with --via, or pass --inventory")
	}
	names, err := c.NodesOf(k8s.KindControlPlane, skip)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("the cluster has no other control-plane node to ask")
	}
	return nodes.ViaControlPlanes(names, tryNextControlPlane, call)
}

// tryNextControlPlane reports whether another control plane may answer where one failed with
// err: one that is down, or one that is no etcd member, as before it joined.
func tryNextControlPlane(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return true
	case connect.CodeFailedPrecondition:
		return notEtcdMember(err)
	}
	return false
}

// notEtcdMember reports whether chalkd refused a request because the node is no etcd member, by
// the NotEtcdMember detail of its error.
func notEtcdMember(err error) bool {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return false
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if _, ok := v.(*kubernetesv1.NotEtcdMember); ok {
				return true
			}
		}
	}
	return false
}

// viaFlags are the flags of the commands that act through a control plane: the etcd commands,
// node revoke and node unrevoke.
type viaFlags struct {
	node  layer.NodeFlags
	via   string
	force bool
}

// etcdCommand is the etcd group and its commands.
func etcdCommand(h layer.Host) *cobra.Command {
	etcd := cli.Group(&cobra.Command{Use: "etcd", Short: "Manage the cluster's etcd members", Long: etcdLong, Example: etcdExample})
	etcd.AddCommand(etcdMembersCommand(h), etcdRemoveMemberCommand(h), etcdLeaveCommand(h))
	return etcd
}

func etcdMembersCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	cmd := h.Command(&cobra.Command{
		Use:     "members",
		Short:   "List etcd's members and their health",
		Long:    etcdMembersLong,
		Example: etcdMembersExample,
		Args:    cli.NoArgs,
	}, func(h layer.Host, ctx context.Context, pos []string) error { return etcdMembers(h, ctx, f) })
	f.node = h.NodeFlags(cmd.Flags())
	cmd.Flags().StringVar(&f.via, "via", "", "control plane to ask (default the first one that answers)")
	return cmd
}

func etcdMembers(h layer.Host, ctx context.Context, f viaFlags) error {
	var members []*kubernetesv1.EtcdMember
	err := throughControlPlane(h, ctx, f, "", func(conn *client.Conn) error {
		resp, err := Client(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return err
	}
	w := h.Table()
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

func etcdRemoveMemberCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	cmd := h.Command(&cobra.Command{
		Use:     "remove-member <node|id>",
		Short:   "Remove a node's etcd member, such as a stale one",
		Long:    etcdRemoveMemberLong,
		Example: etcdRemoveMemberExample,
		Args:    cli.ExactArgs(1),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return etcdRemoveMember(h, ctx, f, pos) })
	fs := cmd.Flags()
	f.node = h.NodeFlags(fs)
	fs.StringVar(&f.via, "via", "", "control plane that removes the member (default the first other one that answers)")
	fs.BoolVar(&f.force, "force", false, "remove the member even when the voters left would have no healthy quorum")
	return cmd
}

func etcdRemoveMember(h layer.Host, ctx context.Context, f viaFlags, pos []string) error {
	var removed *kubernetesv1.EtcdMember
	err := throughControlPlane(h, ctx, f, pos[0], func(conn *client.Conn) error {
		resp, err := Client(conn).EtcdRemoveMember(ctx, connect.NewRequest(&kubernetesv1.EtcdRemoveMemberRequest{Member: pos[0], Force: f.force}))
		if err == nil {
			removed = resp.Msg.Removed
		}
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(h.Stdout(), "removed the etcd member %s (%x)\n", pos[0], removed.Id)
	return nil
}

func etcdLeaveCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	cmd := h.Command(&cobra.Command{
		Use:     "leave <node>",
		Short:   "Take a control plane out of etcd",
		Long:    etcdLeaveLong,
		Example: etcdLeaveExample,
		Args:    cli.ExactArgs(1),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return etcdLeave(h, ctx, f, pos) })
	f.node = h.NodeFlags(cmd.Flags())
	cmd.Flags().BoolVar(&f.force, "force", false, "leave through the other members also when the node's own etcd member does not answer (for example when it lost its pinned address)")
	return cmd
}

func etcdLeave(h layer.Host, ctx context.Context, f viaFlags, pos []string) error {
	t, err := h.Target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	if err := t.Call(func(conn *client.Conn) error {
		_, err := Client(conn).EtcdLeave(ctx, connect.NewRequest(&kubernetesv1.EtcdLeaveRequest{Force: f.force}))
		return err
	}); err != nil {
		return fmt.Errorf("%s: %w", t.Name(), err)
	}
	fmt.Fprintf(h.Stdout(), "%s left etcd; reinstall it to join the cluster again\n", t.Name())
	return nil
}
