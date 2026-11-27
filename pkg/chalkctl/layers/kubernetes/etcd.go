package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
)

// throughControlPlane calls call on the control-plane node via, or else on the cluster's
// control-plane nodes but skip in name order, moving on past nodes that are down or no etcd member.
func throughControlPlane(h layer.Host, ctx context.Context, f viaFlags, skip string, call func(conn *client.Conn) error) error {
	// Host.Nodes refuses a bare address without via too; this error names --via.
	if f.via == "" && f.node.Address.Bare() != "" {
		return errors.New("--address reaches one node; name it with --via")
	}
	nodes, err := h.Nodes(ctx, f.node, f.via)
	if err != nil {
		return err
	}
	if f.via != "" {
		return nodes.ViaFirst([]string{f.via}, func(error) bool { return false }, call)
	}
	c := nodes.Cluster()
	if c.Partial() {
		return errors.New("an inventory names the control planes; name one with --via, or pass --inventory")
	}
	names, err := nodesOf(c, k8s.KindControlPlane, skip)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("the cluster has no other control-plane node to ask")
	}
	return nodes.ViaFirst(names, sdkkubernetes.TryNextControlPlane, call)
}

// viaFlags are the flags of the commands that act through a control plane: the etcd commands,
// node revoke and node unrevoke.
type viaFlags struct {
	node layer.NodeFlags
	via  string
	// override is the flag that overrides the command's check, such as etcd remove-member
	// --ignore-quorum.
	override bool
}

// etcdCommand is the etcd group and its commands.
func etcdCommand(h layer.Host) *cobra.Command {
	etcd := cli.Group(&cobra.Command{Use: "etcd", Short: "Manage the cluster's etcd members", Long: etcdLong, Example: etcdExample})
	etcd.AddCommand(etcdMembersCommand(h), etcdRemoveMemberCommand(h), etcdLeaveCommand(h))
	return etcd
}

func etcdMembersCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	var output layer.Output
	cmd := h.Command(&cobra.Command{
		Use:         "members",
		Short:       "List etcd's members and their health",
		Long:        etcdMembersLong,
		Example:     etcdMembersExample,
		Args:        cli.NoArgs,
		Annotations: layer.Meta{Credentials: layer.Reader}.Annotations(),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return etcdMembers(h, ctx, f, output) })
	f.node.RegisterClient(cmd.Flags())
	output.Register(cmd.Flags())
	cmd.Flags().StringVar(&f.via, "via", "", "control plane to ask, which a bare --address reaches (default the first one that answers)")
	return cmd
}

func etcdMembers(h layer.Host, ctx context.Context, f viaFlags, output layer.Output) error {
	var resp *kubernetesv1.EtcdMembersResponse
	err := throughControlPlane(h, ctx, f, "", layer.Fetch(&resp, func(conn *client.Conn) (*kubernetesv1.EtcdMembersResponse, error) {
		return sdkkubernetes.Client(conn).EtcdMembers(ctx, &kubernetesv1.EtcdMembersRequest{})
	}))
	if err != nil {
		return err
	}
	if output.JSON() {
		return layer.WriteJSON(h.Stdout(), resp)
	}
	w := h.Table()
	fmt.Fprintln(w, "NAME\tID\tPEER URLS\tMEMBER\tHEALTH")
	for _, m := range resp.Members {
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
		Use:         "remove-member <node|id>",
		Short:       "Remove a node's etcd member, such as a stale one",
		Long:        etcdRemoveMemberLong,
		Example:     etcdRemoveMemberExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.Admin}.Annotations(),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return etcdRemoveMember(h, ctx, f, pos) })
	fs := cmd.Flags()
	f.node.RegisterClient(fs)
	fs.StringVar(&f.via, "via", "", "control plane that removes the member, which a bare --address reaches (default the first other one that answers)")
	fs.BoolVar(&f.override, "ignore-quorum", false, "remove the member even when the voters left would have no healthy quorum")
	return cmd
}

func etcdRemoveMember(h layer.Host, ctx context.Context, f viaFlags, pos []string) error {
	var resp *kubernetesv1.EtcdRemoveMemberResponse
	err := throughControlPlane(h, ctx, f, pos[0], layer.Fetch(&resp, func(conn *client.Conn) (*kubernetesv1.EtcdRemoveMemberResponse, error) {
		return sdkkubernetes.EtcdRemoveMember(ctx, conn, pos[0], f.override)
	}))
	if err != nil {
		return err
	}
	fmt.Fprintf(h.Stdout(), "removed the etcd member %s (%x)\n", pos[0], resp.Removed.Id)
	return nil
}

func etcdLeaveCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	cmd := h.Command(&cobra.Command{
		Use:         "leave <node>",
		Short:       "Take a control plane out of etcd",
		Long:        etcdLeaveLong,
		Example:     etcdLeaveExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.Admin}.Annotations(),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return etcdLeave(h, ctx, f, pos) })
	f.node.RegisterClient(cmd.Flags())
	cmd.Flags().BoolVar(&f.override, "through-others", false, "leave through the other members also when the node's own etcd member does not answer (for example when it lost its pinned address)")
	return cmd
}

func etcdLeave(h layer.Host, ctx context.Context, f viaFlags, pos []string) error {
	t, err := h.Target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	if err := t.Call(func(conn *client.Conn) error {
		return sdkkubernetes.EtcdLeave(ctx, conn, f.override)
	}); err != nil {
		return err
	}
	fmt.Fprintf(h.Stdout(), "%s left etcd; reinstall it to join the cluster again\n", t.Name())
	return nil
}
