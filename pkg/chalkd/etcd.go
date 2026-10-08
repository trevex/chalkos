package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"connectrpc.com/connect"
	clientv3 "go.etcd.io/etcd/client/v3"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// controlPlane checks that the node is a control plane and returns its cluster, name and share.
func (k *Kubernetes) controlPlane() (k8s.Cluster, string, kpki.Share, error) {
	if k == nil {
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeFailedPrecondition, "the node's role has no Kubernetes")
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeInternal, "%v", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeFailedPrecondition, "the node is a worker; ask a control-plane node")
	}
	n, err := k8s.ReadNode(k.Paths.NodeFile)
	if err != nil {
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeInternal, "%v", err)
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	return c, n.Name, share, nil
}

// member checks that the node is an etcd member and returns its cluster, name and share.
func (k *Kubernetes) member() (k8s.Cluster, string, kpki.Share, error) {
	c, name, share, err := k.controlPlane()
	if err != nil {
		return k8s.Cluster{}, "", kpki.Share{}, err
	}
	switch bootstrapped, err := knode.Bootstrapped(k.Paths); {
	case err != nil:
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeInternal, "%v", err)
	case !bootstrapped:
		// chalkctl asks the next control plane on this answer.
		return k8s.Cluster{}, "", kpki.Share{}, failed(connect.CodeFailedPrecondition, "the node is not an etcd member; ask another control-plane node")
	}
	return c, name, share, nil
}

// mayBeMember reports whether the node is, or may be, an etcd member: it was bootstrapped, or it
// started joining, or it holds what either left behind.
func mayBeMember(p knode.Paths) (bool, error) {
	pinned := func(p knode.Paths) (bool, error) {
		_, err := os.Stat(p.Pin())
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	for _, marked := range []func(knode.Paths) (bool, error){knode.Bootstrapped, knode.Joining, knode.HasInitialCluster, pinned, knode.EtcdHasData} {
		if ok, err := marked(p); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// dialEtcd connects to the endpoints, the node's own member by default, with chalkd's etcd
// credential.
func (k *Kubernetes) dialEtcd(c k8s.Cluster, share kpki.Share, endpoints ...string) (*clientv3.Client, error) {
	if len(endpoints) == 0 {
		endpoints = []string{k.localEtcd(c)}
	}
	cred, err := newEtcdCredential(share, credentialValidity, time.Now)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := cred.tlsConfig()
	if err != nil {
		return nil, err
	}
	return etcd.Dial(endpoints, tlsConfig)
}

func memberMessage(m etcd.Member, unhealthy error) *nodev1.EtcdMember {
	msg := &nodev1.EtcdMember{Id: m.ID, Name: m.Name, PeerUrls: m.PeerURLs, Learner: m.Learner}
	if unhealthy != nil {
		msg.Unhealthy = unhealthy.Error()
	}
	return msg
}

func (s *Server) EtcdMembers(ctx context.Context, _ *connect.Request[nodev1.EtcdMembersRequest]) (*connect.Response[nodev1.EtcdMembersResponse], error) {
	k := s.Kubernetes
	c, _, share, err := k.member()
	if err != nil {
		return nil, err
	}
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	defer cli.Close()
	ctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	members, err := etcd.Members(ctx, cli)
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	unhealthy := etcd.Health(ctx, cli, members)
	resp := &nodev1.EtcdMembersResponse{}
	for _, m := range members {
		resp.Members = append(resp.Members, memberMessage(m, unhealthy[m.ID]))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) EtcdRemoveMember(ctx context.Context, req *connect.Request[nodev1.EtcdRemoveMemberRequest]) (*connect.Response[nodev1.EtcdRemoveMemberResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.Kubernetes
	c, name, share, err := k.member()
	if err != nil {
		return nil, err
	}
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	members, err := etcd.Members(rctx, cli)
	cancel()
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	m, err := etcd.Find(members, req.Msg.Member)
	if err != nil {
		return nil, failed(connect.CodeNotFound, "%v", err)
	}
	if self, ok, err := k.ownMember(members, name); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	} else if ok && self.ID == m.ID {
		return nil, failed(connect.CodeFailedPrecondition, "%s is this node's own member; chalkctl etcd leave %s takes the node out of etcd", name, name)
	}
	rctx, cancel = k.etcdRequest(ctx)
	err = etcd.Remove(rctx, cli, m.ID, req.Msg.Force)
	cancel()
	if quorum := (*etcd.QuorumError)(nil); errors.As(err, &quorum) {
		return nil, failed(connect.CodeFailedPrecondition, "%v; pass --force to remove it anyway", err)
	}
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	log.Printf("kubernetes: removed the etcd member %s", m)
	return connect.NewResponse(&nodev1.EtcdRemoveMemberResponse{Removed: memberMessage(m, nil)}), nil
}

// ownMember finds the node's member among the members: the one of its name or, as a learner that
// never started has none, one without a name at its pinned peer URL.
func (k *Kubernetes) ownMember(members []etcd.Member, name string) (etcd.Member, bool, error) {
	pin, err := knode.ReadPin(k.Paths)
	if err != nil {
		return etcd.Member{}, false, err
	}
	peerURL := ""
	if pin != nil {
		peerURL = etcd.PeerURL(net.IP(pin[0].AsSlice()))
	}
	for _, m := range members {
		if m.Name == name || (m.Name == "" && peerURL != "" && slices.Contains(m.PeerURLs, peerURL)) {
			return m, true, nil
		}
	}
	return etcd.Member{}, false, nil
}

// leaveEndpoints are the node's own etcd member and the other control planes' that the API
// server lists, which still answer when the node's member was removed.
func (k *Kubernetes) leaveEndpoints(ctx context.Context, c k8s.Cluster, share kpki.Share) []string {
	endpoints := []string{k.localEtcd(c)}
	// Without its addresses the node's own member is listed too, which only repeats it.
	ips, _ := knode.ReadNodeIPs(k.Paths)
	rctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	others, err := k.EtcdEndpoints(rctx, c, share, ips)
	if err != nil {
		log.Printf("kubernetes: leaving etcd through the node's own member alone: %v", err)
		return endpoints
	}
	return append(endpoints, others...)
}

func (s *Server) EtcdLeave(ctx context.Context, req *connect.Request[nodev1.EtcdLeaveRequest]) (*connect.Response[nodev1.EtcdLeaveResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.Kubernetes
	c, name, share, err := k.controlPlane()
	if err != nil {
		return nil, err
	}
	p := k.Paths
	switch member, err := mayBeMember(p); {
	case err != nil:
		return nil, failed(connect.CodeInternal, "%v", err)
	case !member:
		if left, err := knode.Left(p); err == nil && left {
			return nil, failed(connect.CodeFailedPrecondition, "the node left etcd already; reinstall it to join the cluster again")
		}
		return nil, failed(connect.CodeFailedPrecondition, "the node is not an etcd member")
	}
	bootstrapped, err := knode.Bootstrapped(p)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	// A refused leave starts the loops it stopped again.
	restart := false
	defer func() {
		if restart {
			k.Start()
		}
	}()
	stop := func() {
		k.stopLoops()
		restart = true
	}
	// The join holds the membership while it waits for etcd, until its loop stops. A bootstrapped
	// node keeps its loops, and the VIPs, until its leave passed the quorum guard.
	if !bootstrapped {
		stop()
	}
	k.membership.Lock()
	defer k.membership.Unlock()

	endpoints := k.leaveEndpoints(ctx, c, share)
	cli, err := k.dialEtcd(c, share, endpoints...)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	answered, err := etcd.OneCluster(rctx, cli, endpoints)
	cancel()
	if split := (*etcd.SplitError)(nil); errors.As(err, &split) {
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	cli.SetEndpoints(answered...)
	rctx, cancel = k.etcdRequest(ctx)
	members, err := etcd.Members(rctx, cli)
	cancel()
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	self, ok, err := k.ownMember(members, name)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if ok {
		// A bootstrapped node leaves through the other members alone only when forced: its own
		// member may be healthy but cut off, or unable to run, as without its pinned address.
		if bootstrapped && !req.Msg.Force && !slices.Contains(answered, k.localEtcd(c)) {
			return nil, failed(connect.CodeFailedPrecondition, "this node's etcd member %s does not answer at %s; pass --force to remove it through the other members, as when the node lost its pinned address", self, k.localEtcd(c))
		}
		rctx, cancel := k.etcdRequest(ctx)
		err := etcd.CheckQuorum(members, etcd.Health(rctx, cli, members), self.ID)
		cancel()
		if err != nil {
			return nil, failed(connect.CodeFailedPrecondition, "%v", err)
		}
		// The other voters remove the member: this one stops once it is removed.
		var others []string
		for _, m := range members {
			if m.ID != self.ID && !m.Learner {
				others = append(others, m.ClientURLs...)
			}
		}
		if len(others) == 0 {
			return nil, failed(connect.CodeFailedPrecondition, "etcd has no other voter to remove %s through", self)
		}
		remover, err := k.dialEtcd(c, share, others...)
		if err != nil {
			return nil, failed(connect.CodeInternal, "%v", err)
		}
		defer remover.Close()
		// The VIPs go first, while the node is still a healthy member.
		stop()
		rctx, cancel = k.etcdRequest(ctx)
		err = etcd.Remove(rctx, remover, self.ID, false)
		cancel()
		if err != nil {
			return nil, failed(connect.CodeFailedPrecondition, "%v", err)
		}
		log.Printf("kubernetes: removed this node's etcd member %s", self)
	} else {
		// An operator removed it, as while the node joined.
		log.Print("kubernetes: etcd has no member of this node any more")
		stop()
	}
	restart = false

	// A renewal of the control plane's certificates renders the static pods while it holds this
	// lock: it ends first, and none renders them once the node is marked as left.
	unlock, err := knode.LockPKI(p)
	if err != nil {
		return nil, failed(connect.CodeInternal, "the node left etcd, but stopping its control plane failed: %v", err)
	}
	defer unlock()
	// The marker comes first: from here on the node is no member, whatever it still holds.
	if err := knode.MarkLeft(p, time.Now()); err != nil {
		return nil, failed(connect.CodeInternal, "the node left etcd, but recording it failed: %v", err)
	}
	if err := removeStaticPods(p); err != nil {
		return nil, failed(connect.CodeInternal, "the node left etcd, but stopping its control plane failed: %v", err)
	}
	if err := k.EtcdStopped(ctx, c); err != nil {
		return nil, failed(connect.CodeInternal, "the node left etcd, but its etcd did not stop: %v; its data stays in %s until chalkctl etcd leave runs again", err, p.EtcdData)
	}
	if err := knode.RemoveEtcdData(p); err != nil {
		return nil, failed(connect.CodeInternal, "the node left etcd, but deleting its etcd data failed: %v", err)
	}
	if err := knode.ClearMembership(p); err != nil {
		return nil, failed(connect.CodeInternal, "the node left etcd: %v", err)
	}
	log.Print("kubernetes: the node left etcd; reinstall it to join the cluster again")
	return connect.NewResponse(&nodev1.EtcdLeaveResponse{}), nil
}

// removeStaticPods lets the kubelet stop the control plane.
func removeStaticPods(p knode.Paths) error {
	entries, err := os.ReadDir(p.Manifests())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(p.Manifests(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// etcdStopped waits until the node's etcd no longer listens, as the kubelet stops it once its
// static pod is gone.
func etcdStopped(ctx context.Context, c k8s.Cluster) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	addr := net.JoinHostPort(c.Loopback().String(), "2379")
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil && ctx.Err() == nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("etcd still listens on %s: %w", addr, ctx.Err())
		}
		conn.Close()
		select {
		case <-ctx.Done():
			return fmt.Errorf("etcd still listens on %s: %w", addr, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
