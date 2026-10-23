package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/identity"
)

// revokeFlags are the flags of node revoke and node unrevoke.
type revokeFlags struct {
	node  nodeCommand
	via   string
	force bool
}

func (a *app) nodeRevokeCommand() *cobra.Command {
	var f revokeFlags
	cmd := a.command(&cobra.Command{
		Use:     "revoke <node>",
		Short:   "Cut a node off the cluster by name",
		Long:    nodeRevokeLong,
		Example: nodeRevokeExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeRevoke(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	fs := cmd.Flags()
	fs.StringVar(&f.via, "via", "", "control plane that revokes the node (default the first other one that answers)")
	fs.BoolVar(&f.force, "force", false, "revoke the node although it is a control plane: an etcd member, or the one --via names")
	return cmd
}

func (a *app) nodeRevoke(ctx context.Context, f revokeFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl node revoke <node> [--via NODE] [--force]")
	}
	name := pos[0]
	if err := a.keepsRevokedNodes(ctx, f); err != nil {
		return err
	}
	if f.via == name && !f.force {
		return fmt.Errorf("%s is the control plane asked to revoke it; %s", name, controlPlaneRevocation(name))
	}
	var resp *nodev1.RevokeNodeResponse
	// A control plane is not asked to revoke itself.
	err := a.throughControlPlane(ctx, f.node, f.via, name, func(conn *client.Conn) error {
		if !f.force {
			m, err := conn.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
			if err != nil {
				return err
			}
			if err := etcdMemberRefusal(name, m.Msg.Members); err != nil {
				return err
			}
		}
		r, err := conn.RevokeNode(ctx, connect.NewRequest(&nodev1.RevokeNodeRequest{Node: name}))
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
	fmt.Fprintf(a.stdout, "revoked node %s (since %s); %s\n", name, resp.RevokedAt.AsTime().UTC().Format(time.RFC3339), deleted)
	return nil
}

// etcdMemberRefusal refuses to revoke a node that is still an etcd member, a control plane.
func etcdMemberRefusal(name string, members []*nodev1.EtcdMember) error {
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
func (a *app) keepsRevokedNodes(ctx context.Context, f revokeFlags) error {
	if f.via != "" {
		return nil
	}
	c, err := loadCluster(ctx, f.node.cluster)
	if err != nil || c.partial {
		return err
	}
	switch names, err := c.nodesOf(identity.KindControlPlane, ""); {
	case err != nil:
		return err
	case len(names) == 0:
		return fmt.Errorf("the cluster %s has no control-plane node, so it keeps no revoked nodes: "+
			"a node without Kubernetes is revoked by not renewing its node certificate, and chalkctl rotate os-ca cuts it off at once", c.name)
	}
	return nil
}

func (a *app) nodeUnrevokeCommand() *cobra.Command {
	var f revokeFlags
	cmd := a.command(&cobra.Command{
		Use:     "unrevoke <node>",
		Short:   "Let a revoked node's name join the cluster again",
		Long:    nodeUnrevokeLong,
		Example: nodeUnrevokeExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeUnrevoke(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	fs.StringVar(&f.via, "via", "", "control plane that unrevokes the node (default the first one that answers)")
	fs.BoolVar(&f.force, "force", false, "unrevoke the node although the OS CA or the Kubernetes CA predates its revocation, so its old certificates work again")
	return cmd
}

func (a *app) nodeUnrevoke(ctx context.Context, f revokeFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl node unrevoke <node> [--via NODE] [--force]")
	}
	if err := a.keepsRevokedNodes(ctx, f); err != nil {
		return err
	}
	var resp *nodev1.UnrevokeNodeResponse
	err := a.throughControlPlane(ctx, f.node, f.via, "", func(conn *client.Conn) error {
		r, err := conn.UnrevokeNode(ctx, connect.NewRequest(&nodev1.UnrevokeNodeRequest{Node: pos[0], Force: f.force}))
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
	fmt.Fprintln(a.stdout, unrevokedLine(pos[0], at, resp.StaleCas))
	return nil
}

// unrevokedLine says what unrevoking the node revoked at means, with the CAs that predate the
// revocation; at is zero when the time is unknown.
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
	return fmt.Sprintf("unrevoked node %s; its certificates from before %s work again until they expire: the %s %s not rotated since",
		name, when, strings.Join(stale, " and "), verb)
}

// revokedLines are the status lines of the nodes a control plane lists as revoked, or of why
// it could not read them.
func revokedLines(k *nodev1.KubernetesStatus) []string {
	if k.RevokedNodesError != "" {
		return []string{"revoked nodes unknown: " + k.RevokedNodesError}
	}
	if len(k.RevokedNodes) == 0 {
		return nil
	}
	lines := []string{"revoked nodes:"}
	for _, n := range k.RevokedNodes {
		lines = append(lines, "  "+n.Name+"\t"+revokedSince(n))
	}
	return lines
}

// revokedSince says when a node was revoked.
func revokedSince(n *nodev1.RevokedNode) string {
	if n.RevokedAt == nil {
		return "at an unknown time"
	}
	return "since " + n.RevokedAt.AsTime().UTC().Format(time.RFC3339)
}

// revokedNodesOf reads the revoked nodes by name from the first of the control planes whose
// status lists them. A control plane that was never bootstrapped keeps no list, so a cluster
// whose control planes all answer and none of which was bootstrapped has revoked no node yet.
// Otherwise the list is unknown while no control plane gives it: one does not when it does not
// answer, cannot read the list, or does not say it read it, as a chalkd from before revocation.
// The error then names why each did not.
func revokedNodesOf(ctx context.Context, controlPlanes []string, status func(ctx context.Context, name string) (*nodev1.StatusResponse, error)) (map[string]*nodev1.RevokedNode, error) {
	var why []string
	bootstrapped := false
	for _, name := range controlPlanes {
		st, err := status(ctx, name)
		if err != nil {
			bootstrapped = true
			why = append(why, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		switch k := st.Kubernetes; {
		case k == nil || !everBootstrapped(k.State):
			state := "no Kubernetes"
			if k != nil {
				state = k.State
			}
			why = append(why, fmt.Sprintf("%s: is not bootstrapped: %s", name, state))
			continue
		case k.RevokedNodesError != "":
			why = append(why, fmt.Sprintf("%s: %s", name, k.RevokedNodesError))
		case !k.RevokedNodesRead:
			why = append(why, name+": reports no revoked nodes; its chalkd predates node revocation")
		default:
			list := map[string]*nodev1.RevokedNode{}
			for _, n := range k.RevokedNodes {
				list[n.Name] = n
			}
			return list, nil
		}
		bootstrapped = true
	}
	if !bootstrapped {
		return map[string]*nodev1.RevokedNode{}, nil
	}
	return nil, fmt.Errorf("no control-plane node said which nodes are revoked (%s)", strings.Join(why, "; "))
}

// everBootstrapped reports whether a control plane in the Kubernetes state was bootstrapped,
// whether or not it serves now.
func everBootstrapped(state string) bool {
	for _, prefix := range []string{"bootstrapped", "etcd data missing", "left etcd"} {
		if strings.HasPrefix(state, prefix) {
			return true
		}
	}
	return false
}

// notRevoked refuses to deliver the target what the secrets file issues it, which does, while a
// control plane lists it as revoked or while no control plane says which nodes are.
func notRevoked(ctx context.Context, t *target, does string) error {
	controlPlanes, err := t.cluster.nodesOf(identity.KindControlPlane, "")
	if err != nil {
		return err
	}
	list, err := revokedNodesOf(ctx, controlPlanes, func(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
		// The target is reached where the command reaches it, the others where the inventory says.
		ct := t
		if name != t.name {
			if ct, err = targetIn(t.cluster, nodeCommand{}, name, t.creds); err != nil {
				return nil, err
			}
		}
		conn, err := dialInstalled(ct)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return nil, nodeError(err, name)
		}
		return resp.Msg, nil
	})
	if err != nil {
		return fmt.Errorf("%w; %s only once one does, or with --force", err, does)
	}
	if n, ok := list[t.name]; ok {
		return fmt.Errorf("%s is revoked %s, and %s only once chalkctl node unrevoke %s lets it back in, or with --force", t.name, revokedSince(n), does, t.name)
	}
	return nil
}
