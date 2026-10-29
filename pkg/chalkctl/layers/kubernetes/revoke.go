package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

func nodeRevokeCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	cmd := h.Command(&cobra.Command{
		Use:     "revoke <node>",
		Short:   "Cut a node off the cluster by name",
		Long:    nodeRevokeLong,
		Example: nodeRevokeExample,
		Args:    cli.ExactArgs(1),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return nodeRevoke(h, ctx, f, pos) })
	f.node = h.NodeFlags(cmd.Flags())
	fs := cmd.Flags()
	fs.StringVar(&f.via, "via", "", "control plane that revokes the node (default the first other one that answers)")
	fs.BoolVar(&f.force, "force", false, "revoke the node although it is a control plane, an etcd member or the one --via names, or although a control plane does not say it reads the revoked nodes")
	return cmd
}

func nodeRevoke(h layer.Host, ctx context.Context, f viaFlags, pos []string) error {
	name := pos[0]
	if err := keepsRevokedNodes(h, ctx, f); err != nil {
		return err
	}
	if f.via == name && !f.force {
		return fmt.Errorf("%s is the control plane asked to revoke it; %s", name, controlPlaneRevocation(name))
	}
	if !f.force {
		if err := controlPlanesRead(h, ctx, f, name); err != nil {
			return err
		}
	}
	var resp *kubernetesv1.RevokeNodeResponse
	// A control plane is not asked to revoke itself.
	err := throughControlPlane(h, ctx, f, name, func(conn *client.Conn) error {
		if !f.force {
			m, err := Client(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
			if err != nil {
				return err
			}
			if err := etcdMemberRefusal(name, m.Msg.Members); err != nil {
				return err
			}
		}
		r, err := Client(conn).RevokeNode(ctx, connect.NewRequest(&kubernetesv1.RevokeNodeRequest{Node: name}))
		if err == nil {
			resp = r.Msg
		}
		return err
	})
	if err != nil {
		return err
	}
	deleted := "deleted its Node object"
	if !resp.NodeDeleted {
		deleted = "it had no Node object to delete"
	}
	fmt.Fprintf(h.Stdout(), "revoked node %s (since %s); %s\n", name, resp.RevokedAt.AsTime().UTC().Format(time.RFC3339), deleted)
	return nil
}

// controlPlanesRead asks the control planes, but the node revoked, whether they read the revoked
// nodes: --via where the command reaches it, the others where the inventory says, or --via alone
// without an inventory.
func controlPlanesRead(h layer.Host, ctx context.Context, f viaFlags, name string) error {
	c, err := h.Cluster(ctx, f.node)
	if err != nil {
		return err
	}
	if f.via == "" && (c.Partial() || f.node.Address() != "") {
		// node revoke asks for --via, which names the node --address reaches and the control
		// plane to ask without an inventory.
		return nil
	}
	controlPlanes := []string{f.via}
	if !c.Partial() {
		if controlPlanes, err = c.NodesOf(k8s.KindControlPlane, name); err != nil {
			return err
		}
	}
	nodes, err := h.Nodes(ctx, f.node, f.via)
	if err != nil {
		return err
	}
	return everyControlPlaneReads(ctx, controlPlanes, name, NodeStatusOf(nodes))
}

// everyControlPlaneReads refuses to revoke the node while a control plane may renew its node
// certificate unaware of the revocation: one that does not answer, cannot read the revoked
// nodes, or does not say it read them, as a chalkd from before node revocation, which renews
// node certificates without asking. A control plane that is not bootstrapped, or that left etcd,
// renews none.
func everyControlPlaneReads(ctx context.Context, controlPlanes []string, name string, status func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error)) error {
	var why []string
	for _, cp := range controlPlanes {
		k, err := status(ctx, cp)
		if errors.Is(err, ErrNoKubernetes) {
			continue
		}
		if err != nil {
			why = append(why, fmt.Sprintf("%s: %v", cp, err))
			continue
		}
		switch {
		case !everBootstrapped(k) || k.Phase == kubernetesv1.Phase_PHASE_LEFT:
		case k.RevokedNodesError != "":
			why = append(why, fmt.Sprintf("%s: cannot read the revoked nodes: %s", cp, k.RevokedNodesError))
		case !k.RevokedNodesRead:
			why = append(why, fmt.Sprintf("%s: its chalkd predates node revocation and renews %s's node certificate until it is upgraded", cp, name))
		}
	}
	if len(why) == 0 {
		return nil
	}
	return fmt.Errorf("not every control-plane node says it reads the revoked nodes, and one that does not may go on renewing %s's node certificate (%s); "+
		"upgrade or repair those, or pass --force to revoke %s anyway", name, strings.Join(why, "; "), name)
}

// etcdMemberRefusal refuses to revoke a node that is still an etcd member, a control plane.
func etcdMemberRefusal(name string, members []*kubernetesv1.EtcdMember) error {
	for _, m := range members {
		if name != "" && m.Name == name {
			return fmt.Errorf("%s is still an etcd member; %s", name, controlPlaneRevocation(name))
		}
	}
	return nil
}

// controlPlaneRevocation says why revoking a control plane does not cut it off, and what does.
func controlPlaneRevocation(name string) string {
	return fmt.Sprintf("revoking a control plane does not cut it off, because it holds the CA keys: take it out of etcd with "+
		"chalkctl etcd leave %s, or chalkctl etcd remove-member %s if it is gone, then rotate its CAs with chalkctl rotate os-ca "+
		"and chalkctl rotate kubernetes-ca; pass --force to revoke it anyway", name, name)
}

// keepsRevokedNodes refuses a cluster whose inventory names no control plane, which keeps no
// revoked nodes, unless --via names a node to ask.
func keepsRevokedNodes(h layer.Host, ctx context.Context, f viaFlags) error {
	if f.via != "" {
		return nil
	}
	c, err := h.Cluster(ctx, f.node)
	if err != nil || c.Partial() {
		return err
	}
	switch names, err := c.NodesOf(k8s.KindControlPlane, ""); {
	case err != nil:
		return err
	case len(names) == 0:
		return fmt.Errorf("the cluster %s has no control-plane node, so it keeps no revoked nodes: "+
			"a node without Kubernetes is revoked by not renewing its node certificate, and chalkctl rotate os-ca cuts it off at once", c.Name())
	}
	return nil
}

func nodeUnrevokeCommand(h layer.Host) *cobra.Command {
	var f viaFlags
	cmd := h.Command(&cobra.Command{
		Use:     "unrevoke <node>",
		Short:   "Let a revoked node's name join the cluster again",
		Long:    nodeUnrevokeLong,
		Example: nodeUnrevokeExample,
		Args:    cli.ExactArgs(1),
	}, func(h layer.Host, ctx context.Context, pos []string) error { return nodeUnrevoke(h, ctx, f, pos) })
	fs := cmd.Flags()
	f.node = h.NodeFlags(fs)
	fs.StringVar(&f.via, "via", "", "control plane that unrevokes the node (default the first one that answers)")
	fs.BoolVar(&f.force, "force", false, "unrevoke the node although the OS CA or the Kubernetes CA predates its revocation, so its old certificates work again")
	return cmd
}

func nodeUnrevoke(h layer.Host, ctx context.Context, f viaFlags, pos []string) error {
	if err := keepsRevokedNodes(h, ctx, f); err != nil {
		return err
	}
	var resp *kubernetesv1.UnrevokeNodeResponse
	err := throughControlPlane(h, ctx, f, "", func(conn *client.Conn) error {
		r, err := Client(conn).UnrevokeNode(ctx, connect.NewRequest(&kubernetesv1.UnrevokeNodeRequest{Node: pos[0], Force: f.force}))
		if err == nil {
			resp = r.Msg
		}
		return err
	})
	if err != nil {
		return err
	}
	var at time.Time
	if resp.RevokedAt != nil {
		at = resp.RevokedAt.AsTime()
	}
	fmt.Fprintln(h.Stdout(), unrevokedLine(pos[0], at, resp.StaleCas))
	return nil
}

// unrevokedLine says what unrevoking the node revoked at means, with the CAs that predate the
// revocation; at is zero when the time is unknown. When the old node's certificates work again,
// it says how the old node comes back: a kubelet whose Node was deleted never registers it again
// until it restarts.
func unrevokedLine(name string, at time.Time, stale []string) string {
	when := "its revocation, at an unknown time,"
	if !at.IsZero() {
		when = at.UTC().Format(time.RFC3339)
	}
	if len(stale) == 0 {
		return fmt.Sprintf("unrevoked node %s; the certificates it held before %s are no longer trusted", name, when)
	}
	verb := "was"
	if len(stale) > 1 {
		verb = "were"
	}
	return fmt.Sprintf("unrevoked node %s; its certificates from before %s work again until they expire: the %s %s not rotated since\n"+
		"the old %s's kubelet does not register its Node again by itself: reboot it with chalkctl reboot %s, or restart its kubelet; "+
		"a node installed afresh as %s registers as usual",
		name, when, strings.Join(stale, " and "), verb, name, name, name)
}
