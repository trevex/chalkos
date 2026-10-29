package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

func (a *app) nodeRevokeCommand() *cobra.Command {
	var f viaFlags
	cmd := a.command(&cobra.Command{
		Use:     "revoke <node>",
		Short:   "Cut a node off the cluster by name",
		Long:    nodeRevokeLong,
		Example: nodeRevokeExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeRevoke(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	fs := cmd.Flags()
	fs.StringVar(&f.via, "via", "", "control plane that revokes the node (default the first other one that answers)")
	fs.BoolVar(&f.force, "force", false, "revoke the node although it is a control plane, an etcd member or the one --via names, or although a control plane does not say it reads the revoked nodes")
	return cmd
}

func (a *app) nodeRevoke(ctx context.Context, f viaFlags, pos []string) error {
	name := pos[0]
	if err := a.keepsRevokedNodes(ctx, f); err != nil {
		return err
	}
	if f.via == name && !f.force {
		return fmt.Errorf("%s is the control plane asked to revoke it; %s", name, controlPlaneRevocation(name))
	}
	if !f.force {
		if err := a.controlPlanesRead(ctx, f, name); err != nil {
			return err
		}
	}
	var resp *kubernetesv1.RevokeNodeResponse
	// A control plane is not asked to revoke itself.
	err := a.throughControlPlane(ctx, f, name, func(conn *client.Conn) error {
		if !f.force {
			m, err := kubernetesClient(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
			if err != nil {
				return err
			}
			if err := etcdMemberRefusal(name, m.Msg.Members); err != nil {
				return err
			}
		}
		r, err := kubernetesClient(conn).RevokeNode(ctx, connect.NewRequest(&kubernetesv1.RevokeNodeRequest{Node: name}))
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

// controlPlanesRead asks the control planes, but the node revoked, whether they read the revoked
// nodes: --via where the command reaches it, the others where the inventory says, or --via alone
// without an inventory.
func (a *app) controlPlanesRead(ctx context.Context, f viaFlags, name string) error {
	c, err := loadCluster(ctx, f.node.cluster)
	if err != nil {
		return err
	}
	if f.via == "" && (c.partial() || f.node.address != "") {
		// node revoke asks for --via, which names the node --address reaches and the control
		// plane to ask without an inventory.
		return nil
	}
	controlPlanes := []string{f.via}
	if !c.partial() {
		if controlPlanes, err = c.nodesOf(kubernetes.KindControlPlane, name); err != nil {
			return err
		}
	}
	creds, err := a.loadCredentials(ctx, f.node.secrets, f.node.config)
	if err != nil {
		return err
	}
	return everyControlPlaneReads(ctx, controlPlanes, name, f.nodes(c, creds).kubernetesStatus)
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
		if errors.Is(err, errNoKubernetes) {
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
func (a *app) keepsRevokedNodes(ctx context.Context, f viaFlags) error {
	if f.via != "" {
		return nil
	}
	c, err := loadCluster(ctx, f.node.cluster)
	if err != nil || c.partial() {
		return err
	}
	switch names, err := c.nodesOf(kubernetes.KindControlPlane, ""); {
	case err != nil:
		return err
	case len(names) == 0:
		return fmt.Errorf("the cluster %s has no control-plane node, so it keeps no revoked nodes: "+
			"a node without Kubernetes is revoked by not renewing its node certificate, and chalkctl rotate os-ca cuts it off at once", c.name)
	}
	return nil
}

func (a *app) nodeUnrevokeCommand() *cobra.Command {
	var f viaFlags
	cmd := a.command(&cobra.Command{
		Use:     "unrevoke <node>",
		Short:   "Let a revoked node's name join the cluster again",
		Long:    nodeUnrevokeLong,
		Example: nodeUnrevokeExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeUnrevoke(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	fs.StringVar(&f.via, "via", "", "control plane that unrevokes the node (default the first one that answers)")
	fs.BoolVar(&f.force, "force", false, "unrevoke the node although the OS CA or the Kubernetes CA predates its revocation, so its old certificates work again")
	return cmd
}

func (a *app) nodeUnrevoke(ctx context.Context, f viaFlags, pos []string) error {
	if err := a.keepsRevokedNodes(ctx, f); err != nil {
		return err
	}
	var resp *kubernetesv1.UnrevokeNodeResponse
	err := a.throughControlPlane(ctx, f, "", func(conn *client.Conn) error {
		r, err := kubernetesClient(conn).UnrevokeNode(ctx, connect.NewRequest(&kubernetesv1.UnrevokeNodeRequest{Node: pos[0], Force: f.force}))
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

// revokedLines are the status lines of the nodes a control plane lists as revoked, or of why
// it could not read them.
func revokedLines(k *kubernetesv1.StatusResponse) []string {
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
func revokedSince(n *kubernetesv1.RevokedNode) string {
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
func revokedNodesOf(ctx context.Context, controlPlanes []string, status func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error)) (map[string]*kubernetesv1.RevokedNode, error) {
	var why []string
	bootstrapped := false
	for _, name := range controlPlanes {
		k, err := status(ctx, name)
		if errors.Is(err, errNoKubernetes) {
			why = append(why, fmt.Sprintf("%s: is not bootstrapped: no Kubernetes", name))
			continue
		}
		if err != nil {
			bootstrapped = bootstrapped || !errors.As(err, new(notInstalledYet))
			why = append(why, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		switch {
		case !everBootstrapped(k):
			why = append(why, fmt.Sprintf("%s: is not bootstrapped: %s", name, k.State))
			continue
		case k.RevokedNodesError != "":
			why = append(why, fmt.Sprintf("%s: %s", name, k.RevokedNodesError))
		case !k.RevokedNodesRead:
			why = append(why, name+": reports no revoked nodes; its chalkd predates node revocation")
		default:
			list := map[string]*kubernetesv1.RevokedNode{}
			for _, n := range k.RevokedNodes {
				list[n.Name] = n
			}
			return list, nil
		}
		bootstrapped = true
	}
	if !bootstrapped {
		return map[string]*kubernetesv1.RevokedNode{}, nil
	}
	return nil, fmt.Errorf("no control-plane node said which nodes are revoked (%s)", strings.Join(why, "; "))
}

// notInstalledYet is the error of a control plane that did not answer as an installed node, as
// before it is installed: revokedNodesOf counts it among those not bootstrapped.
type notInstalledYet struct{ error }

func (n notInstalledYet) Unwrap() error { return n.error }

// installNotRevoked refuses to install a node under a name a control plane lists as revoked, or
// while control planes were bootstrapped and none says which nodes are revoked. A control plane
// that does not answer as an installed node counts as not installed yet, so the cluster's first
// nodes install while the others wait in maintenance mode or are off.
func installNotRevoked(ctx context.Context, t *target) error {
	controlPlanes, err := t.cluster.nodesOf(kubernetes.KindControlPlane, t.name)
	if err != nil {
		return err
	}
	nodes := nodeSet{cluster: t.cluster, creds: t.creds}
	list, err := revokedNodesOf(ctx, controlPlanes, func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		st, err := nodes.kubernetesStatus(ctx, name)
		switch connect.CodeOf(err) {
		case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
			return nil, notInstalledYet{err}
		}
		return st, err
	})
	if err != nil {
		return fmt.Errorf("%w; install installs %s only once one does, or with --force", err, t.name)
	}
	if n, ok := list[t.name]; ok {
		return fmt.Errorf("%s is revoked %s, and install installs it only once chalkctl node unrevoke %s lets it back in, or with --force", t.name, revokedSince(n), t.name)
	}
	return nil
}

// everBootstrapped reports whether a control plane of the Kubernetes status was bootstrapped,
// whether or not it serves now.
func everBootstrapped(k *kubernetesv1.StatusResponse) bool {
	switch k.GetPhase() {
	case kubernetesv1.Phase_PHASE_BOOTSTRAPPED, kubernetesv1.Phase_PHASE_ETCD_DATA_MISSING, kubernetesv1.Phase_PHASE_LEFT:
		return true
	}
	return false
}

// notRevoked refuses to deliver the target what the secrets file issues it, which does, while a
// control plane lists it as revoked or while no control plane says which nodes are.
func notRevoked(ctx context.Context, t *target, does string) error {
	controlPlanes, err := t.cluster.nodesOf(kubernetes.KindControlPlane, "")
	if err != nil {
		return err
	}
	nodes := nodeSet{cluster: t.cluster, creds: t.creds}
	list, err := revokedNodesOf(ctx, controlPlanes, func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		// The target is reached where the command reaches it, the others where the inventory says.
		if name == t.name {
			return nodeKubernetesStatus(ctx, t)
		}
		return nodes.kubernetesStatus(ctx, name)
	})
	if err != nil {
		return fmt.Errorf("%w; %s only once one does, or with --force", err, does)
	}
	if n, ok := list[t.name]; ok {
		return fmt.Errorf("%s is revoked %s, and %s only once chalkctl node unrevoke %s lets it back in, or with --force", t.name, revokedSince(n), does, t.name)
	}
	return nil
}
