package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

// throughControlPlane calls call on the control-plane node via, or else on the cluster's
// control-plane nodes but skip in name order, moving on past nodes that are down or no etcd member.
func (a *app) throughControlPlane(ctx context.Context, f viaFlags, skip string, call func(conn *client.Conn) error) error {
	c, err := loadCluster(ctx, f.node.cluster)
	if err != nil {
		return err
	}
	creds, err := a.loadCredentials(ctx, f.node.secrets, f.node.config)
	if err != nil {
		return err
	}
	if f.via != "" {
		return f.nodes(c, creds).viaControlPlanes([]string{f.via}, func(error) bool { return false }, call)
	}
	if f.node.address != "" {
		return errors.New("--address reaches one node; name it with --via")
	}
	if c.partial() {
		return errors.New("an inventory names the control planes; name one with --via, or pass --inventory")
	}
	names, err := c.nodesOf(kubernetes.KindControlPlane, skip)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("the cluster has no other control-plane node to ask")
	}
	return f.nodes(c, creds).viaControlPlanes(names, tryNextControlPlane, call)
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
	node  nodeFlags
	via   string
	force bool
}

// nodes reaches the cluster's nodes with the credentials, --via at the address --address gives.
func (f viaFlags) nodes(c *cluster, creds *credentials) nodeSet {
	s := nodeSet{cluster: c, creds: creds}
	if f.via != "" {
		s.addresses = map[string]string{f.via: f.node.address}
	}
	return s
}

func (a *app) etcdMembersCommand() *cobra.Command {
	var f viaFlags
	cmd := a.command(&cobra.Command{
		Use:     "members",
		Short:   "List etcd's members and their health",
		Long:    etcdMembersLong,
		Example: etcdMembersExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.etcdMembers(ctx, f) })
	f.node.registerClient(cmd.Flags())
	cmd.Flags().StringVar(&f.via, "via", "", "control plane to ask (default the first one that answers)")
	return cmd
}

func (a *app) etcdMembers(ctx context.Context, f viaFlags) error {
	var members []*kubernetesv1.EtcdMember
	err := a.throughControlPlane(ctx, f, "", func(conn *client.Conn) error {
		resp, err := kubernetesClient(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return err
	}
	w := a.table()
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
	var f viaFlags
	cmd := a.command(&cobra.Command{
		Use:     "remove-member <node|id>",
		Short:   "Remove a node's etcd member, such as a stale one",
		Long:    etcdRemoveMemberLong,
		Example: etcdRemoveMemberExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.etcdRemoveMember(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	fs.StringVar(&f.via, "via", "", "control plane that removes the member (default the first other one that answers)")
	fs.BoolVar(&f.force, "force", false, "remove the member even when the voters left would have no healthy quorum")
	return cmd
}

func (a *app) etcdRemoveMember(ctx context.Context, f viaFlags, pos []string) error {
	var removed *kubernetesv1.EtcdMember
	err := a.throughControlPlane(ctx, f, pos[0], func(conn *client.Conn) error {
		resp, err := kubernetesClient(conn).EtcdRemoveMember(ctx, connect.NewRequest(&kubernetesv1.EtcdRemoveMemberRequest{Member: pos[0], Force: f.force}))
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
	var f viaFlags
	cmd := a.command(&cobra.Command{
		Use:     "leave <node>",
		Short:   "Take a control plane out of etcd",
		Long:    etcdLeaveLong,
		Example: etcdLeaveExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.etcdLeave(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	cmd.Flags().BoolVar(&f.force, "force", false, "leave through the other members also when the node's own etcd member does not answer (for example when it lost its pinned address)")
	return cmd
}

func (a *app) etcdLeave(ctx context.Context, f viaFlags, pos []string) error {
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	if err := callNode(t, func(conn *client.Conn) error {
		_, err := kubernetesClient(conn).EtcdLeave(ctx, connect.NewRequest(&kubernetesv1.EtcdLeaveRequest{Force: f.force}))
		return err
	}); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	fmt.Fprintf(a.stdout, "%s left etcd; reinstall it to join the cluster again\n", t.name)
	return nil
}
