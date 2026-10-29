package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	kuberneteslayer "github.com/trevex/chalkos/pkg/chalkctl/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/client"
)

// quorum checks that etcd keeps its quorum while the control plane reboots: its other voters
// must be healthy and enough for a quorum. With one or two voters the others can never be enough,
// and the API server is down while it reboots, which only --allow-downtime accepts. etcd may need
// a moment after the control plane before came back, so the check is tried for a while.
func (r *upgradeRun) quorum(ctx context.Context, name string) error {
	return layer.WaitFor(ctx, "", r.quorumWait, r.poll, func() error { return r.quorumOnce(ctx, name) })
}

// quorumOnce checks the quorum once. A reboot keeps every voter, so the quorum counts them all,
// the rebooting one included; etcd.CheckQuorum counts a removal's quorum over the voters left.
// With 4 voters a reboot needs 3 healthy others, a removal 2.
func (r *upgradeRun) quorumOnce(ctx context.Context, name string) error {
	var members []*kubernetesv1.EtcdMember
	err := r.viaControlPlanes(name, kuberneteslayer.TryNextControlPlane, func(conn *client.Conn) error {
		resp, err := kuberneteslayer.Client(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("etcd's members: %w", err)
	}
	voters, healthy, own := 0, 0, false
	for _, m := range members {
		switch {
		case m.Learner:
			return fmt.Errorf("etcd member %s is a learner yet; upgrade once it joined", m.Name)
		case m.Name == name:
			own = true
		case m.Unhealthy == "":
			healthy++
		}
		voters++
	}
	quorum := voters/2 + 1
	switch {
	case !own:
		// A control plane that is no etcd member takes nothing of etcd down.
		return nil
	case voters-1 < quorum && !r.allowDowntime:
		return layer.Permanent{Err: fmt.Errorf("etcd has %s, and without %s fewer than the %d its quorum needs: it and the API server are down while %s reboots; pass --allow-downtime to accept that", layer.Count(voters, "voter"), name, quorum, name)}
	case voters-1 < quorum:
		return nil
	case healthy < quorum:
		return fmt.Errorf("without %s etcd has %s of %d, fewer than the %d its quorum needs", name, layer.Count(healthy, "healthy voter"), voters, quorum)
	}
	return nil
}

// viaControlPlanes calls fn on a control plane: the node itself first when it is one, then the
// others, moving on past those whose error next accepts.
func (r *upgradeRun) viaControlPlanes(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error {
	names := slices.Clone(r.controlPlanes)
	if i := slices.Index(names, prefer); i > 0 {
		names = append([]string{prefer}, slices.Delete(names, i, i+1)...)
	}
	if len(names) == 0 {
		return errors.New("the cluster has no control-plane node to ask; pass --inventory")
	}
	return r.reach.viaControlPlanes(names, next, fn)
}

// drainElsewhere reports whether a drain that failed so may be run on another control plane: not
// after it timed out, when the node's pods may still be stopping and another drain would only
// wait as long again.
func drainElsewhere(err error) bool {
	return connect.CodeOf(err) != connect.CodeDeadlineExceeded && kuberneteslayer.TryNextControlPlane(err)
}

// drain cordons a node of the cluster and evicts its pods.
func (r *upgradeRun) drain(ctx context.Context, n *upgradeNode) error {
	if !n.inCluster {
		return nil
	}
	var resp *kubernetesv1.DrainNodeResponse
	err := r.viaControlPlanes(n.name, drainElsewhere, func(conn *client.Conn) error {
		res, err := kuberneteslayer.Client(conn).DrainNode(ctx, connect.NewRequest(&kubernetesv1.DrainNodeRequest{Node: n.name, TimeoutSeconds: uint32(r.drainTimeout / time.Second), DeleteEmptydirData: r.deleteEmptyDir}))
		if err == nil {
			resp = res.Msg
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("drain %s: %w", n.name, err)
	}
	cordon := "cordoned"
	if !resp.Marked {
		cordon = "was cordoned before and stays so"
	}
	r.say("%s: %s, evicted %s, kept %d", n.name, cordon, layer.Count(len(resp.Evicted), "pod"), len(resp.Kept)+len(resp.Unmanaged))
	if len(resp.Unmanaged) > 0 {
		r.say("%s: keeps pods without a controller: %s; nothing starts them elsewhere, and they are deleted for good if the node stays down longer than their tolerations allow", n.name, strings.Join(resp.Unmanaged, ", "))
	}
	return nil
}

// uncordon makes a node of the cluster schedulable again, when the upgrade cordoned it.
func (r *upgradeRun) uncordon(ctx context.Context, n *upgradeNode) error {
	if !n.inCluster {
		return nil
	}
	var uncordoned bool
	err := r.viaControlPlanes(n.name, kuberneteslayer.TryNextControlPlane, func(conn *client.Conn) error {
		resp, err := kuberneteslayer.Client(conn).UncordonNode(ctx, connect.NewRequest(&kubernetesv1.UncordonNodeRequest{Node: n.name}))
		if err == nil {
			uncordoned = resp.Msg.Uncordoned
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("uncordon %s: %w", n.name, err)
	}
	if uncordoned {
		r.say("%s: uncordoned", n.name)
	}
	return nil
}

// inCluster reports whether a node is part of a cluster, so its Node exists.
func inCluster(k *kubernetesv1.StatusResponse) bool {
	switch k.GetPhase() {
	case kubernetesv1.Phase_PHASE_BOOTSTRAPPED, kubernetesv1.Phase_PHASE_JOINED:
		return true
	}
	return false
}
