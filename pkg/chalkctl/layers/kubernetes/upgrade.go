package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// UpgradeRole reads the node's part in its cluster: a control plane coordinates, and a node that
// is part of the cluster, so its Node exists, is a member.
func (p *Plugin) UpgradeRole(ctx context.Context, conn *client.Conn) (layer.UpgradeRole, error) {
	k, err := Status(ctx, conn)
	if err != nil {
		return layer.UpgradeRole{}, layer.ServiceError(err, p.Name())
	}
	return layer.UpgradeRole{Coordinator: k.Kind == k8s.KindControlPlane, Member: inCluster(k)}, nil
}

// ListedCoordinator reports whether the inventory lists the node as a control plane.
func (p *Plugin) ListedCoordinator(c layer.Cluster, name string) (bool, error) {
	kind, err := c.Kind(name)
	return kind == k8s.KindControlPlane, err
}

// BeforeInstall waits for etcd to keep its quorum while a control plane reboots, and drains a
// worker of the cluster.
func (p *Plugin) BeforeInstall(ctx context.Context, u layer.Upgrading, name string, role layer.UpgradeRole) error {
	if role.Coordinator {
		return quorum(ctx, u, name)
	}
	if role.Member {
		return drain(ctx, u, name)
	}
	return nil
}

// BeforeReboot drains a control plane of the cluster, once it has its image.
func (p *Plugin) BeforeReboot(ctx context.Context, u layer.Upgrading, name string, role layer.UpgradeRole) error {
	if role.Coordinator && role.Member {
		return drain(ctx, u, name)
	}
	return nil
}

// Ready checks that the Node of a node of the cluster is Ready.
func (p *Plugin) Ready(ctx context.Context, conn *client.Conn, role layer.UpgradeRole) error {
	if !role.Member {
		return nil
	}
	k, err := Status(ctx, conn)
	if err != nil {
		return err
	}
	if k.GetNodeReady() != "True" {
		return fmt.Errorf("its Node is not Ready: %s", k.GetNodeReady())
	}
	return nil
}

// After uncordons a node of the cluster, when the upgrade cordoned it.
func (p *Plugin) After(ctx context.Context, u layer.Upgrading, name string, role layer.UpgradeRole) error {
	if !role.Member {
		return nil
	}
	return uncordon(ctx, u, name)
}

// quorum checks that etcd keeps its quorum while the control plane reboots: its other voters
// must be healthy and enough for a quorum. With one or two voters the others can never be enough,
// and the API server is down while it reboots, which only --allow-downtime accepts. etcd may need
// a moment after the control plane before came back, so the check is tried for a while.
func quorum(ctx context.Context, u layer.Upgrading, name string) error {
	return layer.WaitFor(ctx, "", u.QuorumWait(), u.Poll(), func() error { return quorumOnce(ctx, u, name) })
}

// quorumOnce checks the quorum once. A reboot keeps every voter, so the quorum counts them all,
// the rebooting one included; etcd.CheckQuorum counts a removal's quorum over the voters left.
// With 4 voters a reboot needs 3 healthy others, a removal 2.
func quorumOnce(ctx context.Context, u layer.Upgrading, name string) error {
	var members []*kubernetesv1.EtcdMember
	err := u.ViaCoordinators(name, tryNextControlPlane, func(conn *client.Conn) error {
		resp, err := Client(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
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
	case voters-1 < quorum && !u.AllowDowntime():
		return layer.Permanent{Err: fmt.Errorf("etcd has %s, and without %s fewer than the %d its quorum needs: it and the API server are down while %s reboots; pass --allow-downtime to accept that", layer.Count(voters, "voter"), name, quorum, name)}
	case voters-1 < quorum:
		return nil
	case healthy < quorum:
		return fmt.Errorf("without %s etcd has %s of %d, fewer than the %d its quorum needs", name, layer.Count(healthy, "healthy voter"), voters, quorum)
	}
	return nil
}

// drainElsewhere reports whether a drain that failed so may be run on another control plane: not
// after it timed out, when the node's pods may still be stopping and another drain would only
// wait as long again.
func drainElsewhere(err error) bool {
	return connect.CodeOf(err) != connect.CodeDeadlineExceeded && tryNextControlPlane(err)
}

// drain cordons a node of the cluster and evicts its pods.
func drain(ctx context.Context, u layer.Upgrading, name string) error {
	var resp *kubernetesv1.DrainNodeResponse
	err := u.ViaCoordinators(name, drainElsewhere, func(conn *client.Conn) error {
		res, err := Client(conn).DrainNode(ctx, connect.NewRequest(&kubernetesv1.DrainNodeRequest{Node: name, TimeoutSeconds: uint32(u.DrainTimeout() / time.Second), DeleteEmptydirData: u.DeleteEmptyDir()}))
		if err == nil {
			resp = res.Msg
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("drain %s: %w", name, err)
	}
	cordon := "cordoned"
	if !resp.Marked {
		cordon = "was cordoned before and stays so"
	}
	u.Say("%s: %s, evicted %s, kept %d", name, cordon, layer.Count(len(resp.Evicted), "pod"), len(resp.Kept)+len(resp.Unmanaged))
	if len(resp.Unmanaged) > 0 {
		u.Say("%s: keeps pods without a controller: %s; nothing starts them elsewhere, and they are deleted for good if the node stays down longer than their tolerations allow", name, strings.Join(resp.Unmanaged, ", "))
	}
	return nil
}

// uncordon makes a node of the cluster schedulable again, when the upgrade cordoned it.
func uncordon(ctx context.Context, u layer.Upgrading, name string) error {
	var uncordoned bool
	err := u.ViaCoordinators(name, tryNextControlPlane, func(conn *client.Conn) error {
		resp, err := Client(conn).UncordonNode(ctx, connect.NewRequest(&kubernetesv1.UncordonNodeRequest{Node: name}))
		if err == nil {
			uncordoned = resp.Msg.Uncordoned
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("uncordon %s: %w", name, err)
	}
	if uncordoned {
		u.Say("%s: uncordoned", name)
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
