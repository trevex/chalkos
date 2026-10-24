package chalkd

import (
	"context"
	"log"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
)

func (s *Server) Bootstrap(ctx context.Context, _ *connect.Request[nodev1.BootstrapRequest]) (*connect.Response[nodev1.BootstrapResponse], error) {
	k := s.Kubernetes
	if k == nil {
		return nil, failed(connect.CodeFailedPrecondition, "the node's image has no Kubernetes")
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return nil, failed(connect.CodeFailedPrecondition, "the node is a worker; bootstrap a control-plane node")
	}
	s.mu.Lock()
	done, err := s.bootstrap(ctx, k, c)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, failed(connect.CodeDeadlineExceeded, "etcd is initialised and the control plane starts, but chalkos's add-ons and the objects of the Addon documents were not applied yet; chalkctl status shows its progress")
	}
	k.mu.Lock()
	n := k.count
	k.mu.Unlock()
	return connect.NewResponse(&nodev1.BootstrapResponse{Applied: uint32(n)}), nil
}

// bootstrap commits the node to initialising etcd: the marker comes first, so a failure after
// it is retried by the next boot and never initialises a second cluster.
func (s *Server) bootstrap(ctx context.Context, k *Kubernetes, c k8s.Cluster) (chan struct{}, error) {
	m, leftovers, err := knode.ReadMembership(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	switch {
	case m == knode.MembershipBootstrapped:
		return nil, failed(connect.CodeFailedPrecondition, "the node is bootstrapped already")
	case m == knode.MembershipLeft:
		return nil, failed(connect.CodeFailedPrecondition, "the node left etcd; reinstall it to join the cluster again")
	case leftovers.EtcdData:
		return nil, failed(connect.CodeFailedPrecondition, "%s holds etcd data; reset VAR before bootstrapping a new cluster on this node", k.Paths.EtcdData)
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	// The static pods advertise the node's address and read the certificates the preparation
	// writes, which runs at boot while chalkd answers already.
	switch problem, err := preparation(k.Paths); {
	case err != nil:
		return nil, failed(connect.CodeInternal, "%v", err)
	case problem == preparing:
		return nil, failed(connect.CodeFailedPrecondition, "%v; see chalkctl logs %s --unit chalkos-kubernetes", knode.ErrNotPrepared, s.nodeName())
	case problem != "":
		return nil, failed(connect.CodeFailedPrecondition, "%s", problem)
	}
	ips, err := knode.ReadNodeIPs(k.Paths)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if !k.membership.TryLock() {
		return nil, failed(connect.CodeFailedPrecondition, "the node is joining the cluster at %s; chalkctl status shows its progress", c.Endpoint)
	}
	defer k.membership.Unlock()
	// A join may have run while the node waited for the lock.
	if m, leftovers, err = knode.ReadMembership(k.Paths); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	switch {
	case m == knode.MembershipBootstrapped:
		return nil, failed(connect.CodeFailedPrecondition, "the node joined the cluster already")
	case m == knode.MembershipLeft:
		return nil, failed(connect.CodeFailedPrecondition, "the node left etcd; reinstall it to join the cluster again")
	case m == knode.MembershipJoining:
		// The node may be an etcd member of the cluster already, even when it never learned so.
		return nil, failed(connect.CodeFailedPrecondition, "the node started joining the cluster at %s; finish the join or remove its member and reinstall", c.Endpoint)
	case leftovers.InitialCluster:
		return nil, failed(connect.CodeFailedPrecondition, "the node is joining the cluster at %s; chalkctl status shows its progress", c.Endpoint)
	}
	// A second bootstrap would start a second cluster.
	if k.hooks.ClusterAnswers(ctx, c, share) {
		return nil, failed(connect.CodeFailedPrecondition, "the cluster's API server answers at %s; this node joins it on its own", c.Endpoint)
	}
	// etcd's peers and the certificates know the node by its addresses from now on.
	if err := knode.WritePin(k.Paths, ips); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if err := knode.MarkBootstrapped(k.Paths, time.Now()); err != nil {
		return nil, failed(connect.CodeInternal, "record the bootstrap: %v", err)
	}
	if err := knode.RenderStaticPods(k.Paths); err != nil {
		return nil, failed(connect.CodeInternal, "render the static pods: %v", err)
	}
	log.Print("bootstrapped: the control plane starts")
	return k.start(), nil
}
