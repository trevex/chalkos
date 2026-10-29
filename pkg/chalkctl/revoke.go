package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	kuberneteslayer "github.com/trevex/chalkos/pkg/chalkctl/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

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
		if errors.Is(err, kuberneteslayer.ErrNoKubernetes) {
			why = append(why, fmt.Sprintf("%s: is not bootstrapped: no Kubernetes", name))
			continue
		}
		if err != nil {
			bootstrapped = bootstrapped || !errors.As(err, new(notInstalledYet))
			why = append(why, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		switch {
		case !kuberneteslayer.EverBootstrapped(k):
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
		return fmt.Errorf("%s is revoked %s, and install installs it only once chalkctl node unrevoke %s lets it back in, or with --force", t.name, kuberneteslayer.RevokedSince(n), t.name)
	}
	return nil
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
		return fmt.Errorf("%s is revoked %s, and %s only once chalkctl node unrevoke %s lets it back in, or with --force", t.name, kuberneteslayer.RevokedSince(n), does, t.name)
	}
	return nil
}
