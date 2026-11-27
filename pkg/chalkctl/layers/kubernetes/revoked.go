package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/strs"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
	kubernetesview "github.com/trevex/chalkos/pkg/statusview/layers/kubernetes"
)

// revokedNodesOf reads the revoked nodes by name from the first of the control planes whose
// status lists them. A control plane that was never bootstrapped keeps no list, so a cluster
// whose control planes all answer and none of which was bootstrapped has revoked no node yet.
// Otherwise the list is unknown while no control plane gives it: one does not when it does not
// answer, left etcd or lost its etcd data, or cannot read the list. The error then names why each
// did not.
func revokedNodesOf(ctx context.Context, controlPlanes []string, status statusFunc) (map[string]*kubernetesv1.RevokedNode, error) {
	var why []string
	bootstrapped := false
	for _, name := range controlPlanes {
		k, err := status(ctx, name)
		if errors.Is(err, sdkkubernetes.ErrNoKubernetes) {
			why = append(why, fmt.Sprintf("%s: is not bootstrapped: no Kubernetes", name))
			continue
		}
		if err != nil {
			bootstrapped = bootstrapped || !errors.As(err, new(notInstalledYet))
			why = append(why, layer.Named(name, err).Error())
			continue
		}
		switch {
		case !everBootstrapped(k):
			why = append(why, fmt.Sprintf("%s: is not bootstrapped: %s", name, k.State))
			continue
		case k.Phase != kubernetesv1.Phase_PHASE_BOOTSTRAPPED:
			why = append(why, name+": "+k.State)
		case k.RevokedNodesError != "":
			why = append(why, fmt.Sprintf("%s: %s", name, k.RevokedNodesError))
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

// AdmitInstall refuses to install a node under a name a control plane lists as revoked, or
// while control planes were bootstrapped and none says which nodes are revoked. A control plane
// that does not answer as an installed node counts as not installed yet, so the cluster's first
// nodes install while the others wait in maintenance mode or are off.
func (p *Plugin) AdmitInstall(ctx context.Context, t layer.Target) error {
	status := nodeStatusOf(t.Nodes().Call)
	return admit(ctx, t, t.Name(), func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		st, err := status(ctx, name)
		switch connect.CodeOf(err) {
		case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
			return nil, notInstalledYet{err}
		}
		return st, err
	}, "install installs "+t.Name())
}

// Admit refuses to deliver the target what the secrets file issues it, which does says, while
// a control plane lists it as revoked or while no control plane says which nodes are.
func (p *Plugin) Admit(ctx context.Context, t layer.Target, does string) error {
	status := nodeStatusOf(t.Nodes().Call)
	// The target is reached where the command reaches it, the others where the inventory says.
	target := nodeStatusOf(func(_ string, fn func(conn *client.Conn) error) error { return t.Call(fn) })
	return admit(ctx, t, "", func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		if name == t.Name() {
			return target(ctx, name)
		}
		return status(ctx, name)
	}, does)
}

// admit refuses what does says while a control plane other than skip lists the target as
// revoked, or while none of them says which nodes are.
func admit(ctx context.Context, t layer.Target, skip string, status statusFunc, does string) error {
	controlPlanes, err := nodesOf(t.Cluster(), k8s.KindControlPlane, skip)
	if err != nil {
		return err
	}
	list, err := revokedNodesOf(ctx, controlPlanes, status)
	if err != nil {
		return fmt.Errorf("%w; %s only once one does, or with --ignore-revoked", err, does)
	}
	if n, ok := list[t.Name()]; ok {
		return fmt.Errorf("%s is revoked %s, and %s only once chalkctl node unrevoke %s lets it back in, or with --ignore-revoked", t.Name(), kubernetesview.RevokedSince(n), does, t.Name())
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

// RefreshRevoked lists those of names a control plane lists as revoked as revoked now, each
// through the first control plane that answers, the node itself last. It reads the list as Admit
// does. A cluster without control planes keeps no revoked nodes, and a node unrevoked in between
// is left alone.
func (p *Plugin) RefreshRevoked(ctx context.Context, nodes layer.Nodes, names []string) ([]string, error) {
	controlPlanes, err := nodesOf(nodes.Cluster(), k8s.KindControlPlane, "")
	if err != nil || len(controlPlanes) == 0 || len(names) == 0 {
		return nil, err
	}
	list, err := revokedNodesOf(ctx, controlPlanes, nodeStatusOf(nodes.Call))
	if err != nil {
		return nil, unknownRevoked(names, err)
	}
	var lines, failed, why []string
	for _, name := range names {
		if _, ok := list[name]; !ok {
			continue
		}
		via := othersFirst(controlPlanes, name)
		var resp *kubernetesv1.RevokeNodeResponse
		err := nodes.ViaFirst(via, sdkkubernetes.TryNextControlPlane, layer.Fetch(&resp, func(conn *client.Conn) (*kubernetesv1.RevokeNodeResponse, error) {
			return sdkkubernetes.RefreshRevocation(ctx, conn, name)
		}))
		switch {
		case sdkkubernetes.NotRevoked(err):
		case err != nil:
			failed, why = append(failed, name), append(why, err.Error())
		default:
			lines = append(lines, refreshedLine(name, resp))
		}
	}
	if len(failed) == 0 {
		return lines, nil
	}
	if len(failed) == 1 {
		return lines, fmt.Errorf("the revocation of %s, which was handed certificates while revoked, was not refreshed (%s); pruning may drop it from the revoked nodes while they are valid, so refresh it with %s",
			failed[0], why[0], refreshCommand(failed[0]))
	}
	return lines, fmt.Errorf("the revocations of %s, which were handed certificates while revoked, were not refreshed (%s); pruning may drop them from the revoked nodes while those are valid, so refresh each with %s",
		strs.And(failed), strings.Join(why, "; "), refreshCommand("<node>"))
}

// unknownRevoked is the error of a refresh of the names' revocations while no control plane says
// which nodes are revoked.
func unknownRevoked(names []string, err error) error {
	if len(names) == 1 {
		return fmt.Errorf("%w; whether %s is revoked is unknown, so its revocation was not refreshed after it was handed certificates: if it is revoked, pruning may drop it from the revoked nodes while they are valid, so once a control plane answers, refresh it with %s",
			err, names[0], refreshCommand(names[0]))
	}
	return fmt.Errorf("%w; which of %s are revoked is unknown, so no revocation was refreshed after they were handed certificates: pruning may drop a revoked one from the revoked nodes while those are valid, so once a control plane answers, refresh each revoked one with %s",
		err, strs.And(names), refreshCommand("<node>"))
}

// refreshCommand is the command that refreshes the revocation of node.
func refreshCommand(node string) string {
	return "chalkctl node revoke " + node + " --refresh"
}
