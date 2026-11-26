package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"slices"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/internal/poll"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// controlPlaneNode is what a request to a control-plane node reads of it.
type controlPlaneNode struct {
	cluster k8s.Cluster
	name    string
	share   kpki.Share
}

// controlPlane checks that the node is a control plane and reads its cluster, name and share. Its
// errors carry their codes, which callers such as notRevoked keep rather than give one of their own.
func (k *side) controlPlane() (controlPlaneNode, error) {
	c, err := k8s.ReadCluster(k.state.Cluster)
	if err != nil {
		return controlPlaneNode{}, layer.Errorf(connect.CodeInternal, "%w", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return controlPlaneNode{}, layer.Errorf(connect.CodeFailedPrecondition, "the node is a worker; ask a control-plane node")
	}
	n, err := k8s.ReadNode(k.state.NodeFile)
	if err != nil {
		return controlPlaneNode{}, layer.Errorf(connect.CodeInternal, "%w", err)
	}
	share, err := k.state.ReadShare()
	if err != nil {
		return controlPlaneNode{}, layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
	}
	return controlPlaneNode{cluster: c, name: n.Name, share: share}, nil
}

// etcdMember checks that the node is an etcd member and reads its cluster, name and share.
func (k *side) etcdMember() (controlPlaneNode, error) {
	cp, err := k.controlPlane()
	if err != nil {
		return controlPlaneNode{}, err
	}
	switch m, _, err := k.state.ReadMembership(); {
	case err != nil:
		return controlPlaneNode{}, layer.Errorf(connect.CodeInternal, "%w", err)
	case m != knode.MembershipBootstrapped:
		return controlPlaneNode{}, notEtcdMemberError{}
	}
	return cp, nil
}

// notEtcdMemberError refuses a request that needs an etcd member. Its NotEtcdMember detail tells
// clients, such as chalkctl, to ask the next control-plane node.
type notEtcdMemberError struct{}

func (notEtcdMemberError) Error() string {
	return "the node is not an etcd member; ask another control-plane node"
}

func (notEtcdMemberError) Code() connect.Code { return connect.CodeFailedPrecondition }

func (notEtcdMemberError) Details() []*connect.ErrorDetail {
	detail, err := connectproto.NewErrorDetail(&kubernetesv1.NotEtcdMember{})
	if err != nil {
		return nil
	}
	return []*connect.ErrorDetail{detail}
}

// mayBeMember reports whether the node is, or may be, an etcd member: it was bootstrapped, or it
// started joining, or it holds what either left behind.
func mayBeMember(m knode.Membership, l knode.Leftovers) bool {
	return m == knode.MembershipBootstrapped || m == knode.MembershipJoining || holdsMembership(l)
}

// holdsMembership reports whether the node holds what an etcd membership leaves behind.
func holdsMembership(l knode.Leftovers) bool { return l.InitialCluster || l.Pinned || l.EtcdData }

// dialEtcd connects to the endpoints, the node's own member by default, with chalkd's etcd
// credential.
func (k *side) dialEtcd(c k8s.Cluster, share kpki.Share, endpoints ...string) (*etcd.Client, error) {
	if len(endpoints) == 0 {
		endpoints = []string{k.localEtcd(c)}
	}
	cred, err := newEtcdCredential(share, time.Now)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := cred.tlsConfig()
	if err != nil {
		return nil, err
	}
	cli, err := etcd.Dial(endpoints, tlsConfig)
	if err != nil {
		return nil, err
	}
	cli.StatusTimeout = k.timing.EtcdStatusTimeout
	cli.RetryInterval = k.timing.EtcdRetry
	return cli, nil
}

func memberMessage(m etcd.Member, unhealthy error) *kubernetesv1.EtcdMember {
	msg := &kubernetesv1.EtcdMember{Id: m.ID, Name: m.Name, PeerUrls: m.PeerURLs, Learner: m.Learner}
	if unhealthy != nil {
		msg.Unhealthy = unhealthy.Error()
	}
	return msg
}

func (p *Plugin) EtcdMembers(ctx context.Context, _ *kubernetesv1.EtcdMembersRequest) (*kubernetesv1.EtcdMembersResponse, error) {
	k := p.k
	cp, err := k.etcdMember()
	if err != nil {
		return nil, err
	}
	cli, err := k.dialEtcd(cp.cluster, cp.share)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	ctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	members, err := cli.Members(ctx)
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	unhealthy := cli.Health(ctx, members)
	resp := &kubernetesv1.EtcdMembersResponse{}
	for _, m := range members {
		resp.Members = append(resp.Members, memberMessage(m, unhealthy[m.ID]))
	}
	return resp, nil
}

func (p *Plugin) EtcdRemoveMember(ctx context.Context, req *kubernetesv1.EtcdRemoveMemberRequest) (*kubernetesv1.EtcdRemoveMemberResponse, error) {
	p.node.Changes().Lock()
	defer p.node.Changes().Unlock()
	k := p.k
	cp, err := k.etcdMember()
	if err != nil {
		return nil, err
	}
	name := cp.name
	cli, err := k.dialEtcd(cp.cluster, cp.share)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	members, err := cli.Members(rctx)
	cancel()
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	m, err := etcd.Find(members, req.Member)
	if err != nil {
		return nil, layer.Errorf(connect.CodeNotFound, "%w", err)
	}
	if self, ok, err := k.ownMember(members, name); err != nil {
		return nil, err
	} else if ok && self.ID == m.ID {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "%s is this node's own member; chalkctl etcd leave %s takes the node out of etcd", name, name)
	}
	rctx, cancel = k.etcdRequest(ctx)
	err = cli.Remove(rctx, m.ID, req.Force)
	cancel()
	if _, ok := errors.AsType[*etcd.QuorumError](err); ok {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "%w; pass --ignore-quorum to remove it anyway", err)
	}
	if err != nil {
		return nil, removeError(err)
	}
	log.Printf("kubernetes: removed the etcd member %s", m)
	return &kubernetesv1.EtcdRemoveMemberResponse{Removed: memberMessage(m, nil)}, nil
}

// removeError gives a failed removal of a member its code: etcd that does not answer, or not in
// time, is UNAVAILABLE, so chalkctl asks another member; etcd that refuses is FAILED_PRECONDITION.
func removeError(err error) error {
	code := status.Code(err)
	if e, ok := errors.AsType[rpctypes.EtcdError](err); ok {
		code = e.Code()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		code == codes.Unavailable || code == codes.DeadlineExceeded {
		return layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	return layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
}

// ownMember finds the node's member among the members with etcd.Self, at the peer URL of the
// node's pinned address.
func (k *side) ownMember(members []etcd.Member, name string) (etcd.Member, bool, error) {
	pin, err := k.state.ReadPin()
	if err != nil {
		return etcd.Member{}, false, err
	}
	peerURL := ""
	if pin != nil {
		peerURL = k8s.EtcdPeerURL(pin[0])
	}
	m, ok := etcd.Self(members, name, peerURL)
	return m, ok, nil
}

// leaveEndpoints are the node's own etcd member and the other control planes' members the API
// server lists. The others still answer once the node's member is removed.
func (k *side) leaveEndpoints(ctx context.Context, c k8s.Cluster, share kpki.Share) []string {
	endpoints := []string{k.localEtcd(c)}
	// Without its addresses the node's own member is listed too, which only repeats it.
	ips, _ := k.state.ReadNodeIPs()
	rctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	others, err := k.hooks.EtcdEndpoints(rctx, c, share, ips)
	if err != nil {
		log.Printf("kubernetes: leaving etcd through the node's own member alone: %v", err)
		return endpoints
	}
	return append(endpoints, others...)
}

func (p *Plugin) EtcdLeave(ctx context.Context, req *kubernetesv1.EtcdLeaveRequest) (*kubernetesv1.EtcdLeaveResponse, error) {
	p.node.Changes().Lock()
	defer p.node.Changes().Unlock()
	k := p.k
	cp, err := k.controlPlane()
	if err != nil {
		return nil, err
	}
	m, leftovers, err := k.state.ReadMembership()
	switch {
	case err != nil:
		return nil, err
	case m == knode.MembershipLeft && holdsMembership(leftovers):
		// etcd removed the member already: what is left is local, and the cluster need not answer.
		k.membership.Lock()
		defer k.membership.Unlock()
		err = k.finishLeave(ctx, cp.cluster)
	case m == knode.MembershipLeft:
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the node left etcd already; reinstall it to join the cluster again")
	case mayBeMember(m, leftovers):
		err = k.leaveEtcd(ctx, cp, m == knode.MembershipBootstrapped, req.Force)
	default:
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "the node is not an etcd member")
	}
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.EtcdLeaveResponse{}, nil
}

// leaveEtcd removes the node's member through the other members, then finishes the leave.
func (k *side) leaveEtcd(ctx context.Context, cp controlPlaneNode, bootstrapped, force bool) error {
	c, name, share := cp.cluster, cp.name, cp.share
	// A refused leave starts the loops it stopped again.
	restart := false
	defer func() {
		if restart {
			k.startLoops()
		}
	}()
	stop := func() {
		// The VIPs must be released before the node leaves, so the wait outlives the request.
		k.stopLoops(context.WithoutCancel(ctx))
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
		return err
	}
	defer cli.Close()
	rctx, cancel := k.etcdRequest(ctx)
	answered, err := cli.OneCluster(rctx, endpoints)
	cancel()
	if _, ok := errors.AsType[*etcd.SplitError](err); ok {
		return layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
	}
	if err != nil {
		return layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	cli.SetEndpoints(answered...)
	rctx, cancel = k.etcdRequest(ctx)
	members, err := cli.Members(rctx)
	cancel()
	if err != nil {
		return layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	self, ok, err := k.ownMember(members, name)
	if err != nil {
		return err
	}
	if ok {
		// A bootstrapped node leaves through the other members alone only when forced: its own
		// member may be healthy but cut off, or unable to run, as without its pinned address.
		if bootstrapped && !force && !slices.Contains(answered, k.localEtcd(c)) {
			return layer.Errorf(connect.CodeFailedPrecondition, "this node's etcd member %s does not answer at %s; pass --through-others to remove it through the other members, as when the node lost its pinned address", self, k.localEtcd(c))
		}
		rctx, cancel := k.etcdRequest(ctx)
		err := etcd.CheckQuorum(members, cli.Health(rctx, members), self.ID)
		cancel()
		if err != nil {
			return layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
		}
		// The other voters remove the member: this one stops once it is removed.
		var others []string
		for _, m := range members {
			if m.ID != self.ID && !m.Learner {
				others = append(others, m.ClientURLs...)
			}
		}
		if len(others) == 0 {
			return layer.Errorf(connect.CodeFailedPrecondition, "etcd has no other voter to remove %s through", self)
		}
		remover, err := k.dialEtcd(c, share, others...)
		if err != nil {
			return err
		}
		defer remover.Close()
		// The VIPs go first, while the node is still a healthy member.
		stop()
		rctx, cancel = k.etcdRequest(ctx)
		err = remover.Remove(rctx, self.ID, false)
		cancel()
		if err != nil {
			return removeError(err)
		}
		log.Printf("kubernetes: removed this node's etcd member %s", self)
	} else {
		// An operator removed it, as while the node joined.
		log.Print("kubernetes: etcd has no member of this node any more")
		stop()
	}
	restart = false
	return k.finishLeave(ctx, c)
}

// finishLeave runs the local steps of a leave once etcd has no member of the node: it marks the
// node as left, stops its control plane and etcd, and deletes what the membership left behind.
// The caller holds the membership lock.
func (k *side) finishLeave(ctx context.Context, c k8s.Cluster) error {
	// A renewal of the control plane's certificates renders the static pods while it holds this
	// lock: it ends first, and none renders them once the node is marked as left.
	unlock, err := k.state.LockPKI(ctx)
	if err != nil {
		return fmt.Errorf("the node left etcd, but stopping its control plane failed: %w", err)
	}
	defer unlock()
	// The marker comes first: from here on the node is no member, whatever it still holds.
	if err := k.state.MarkLeft(time.Now()); err != nil {
		return fmt.Errorf("the node left etcd, but recording it failed: %w", err)
	}
	if err := k.state.RemoveStaticPods(); err != nil {
		return fmt.Errorf("the node left etcd, but stopping its control plane failed: %w", err)
	}
	if err := k.hooks.EtcdStopped(ctx, c); err != nil {
		return fmt.Errorf("the node left etcd, but its etcd did not stop: %w; its data stays in %s until chalkctl etcd leave runs again", err, k.state.EtcdData)
	}
	if err := k.state.RemoveEtcdData(); err != nil {
		return fmt.Errorf("the node left etcd, but deleting its etcd data failed: %w", err)
	}
	if err := k.state.ClearMembership(); err != nil {
		return fmt.Errorf("the node left etcd: %w", err)
	}
	log.Print("kubernetes: the node left etcd; reinstall it to join the cluster again")
	return nil
}

// etcdStopped waits until the node's etcd no longer listens, as the kubelet stops it once its
// static pod is gone.
func etcdStopped(ctx context.Context, c k8s.Cluster) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u, err := url.Parse(c.LocalEtcd())
	if err != nil {
		return err
	}
	addr := u.Host
	return poll.UntilDone(ctx, "", time.Second, func(ctx context.Context) error {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil && ctx.Err() == nil {
			return nil
		}
		if err == nil {
			conn.Close()
		}
		return fmt.Errorf("etcd still listens on %s", addr)
	})
}
