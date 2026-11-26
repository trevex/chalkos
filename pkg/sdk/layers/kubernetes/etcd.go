package kubernetes

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/client"
)

// TryNextControlPlane reports whether another control plane may answer where one failed with
// err: one that is down, or one that is no etcd member, as before it joined.
func TryNextControlPlane(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return true
	case connect.CodeFailedPrecondition:
		return NotEtcdMember(err)
	}
	return false
}

// NotEtcdMember reports whether chalkd refused a request because the node is no etcd member, by
// the NotEtcdMember detail of its error.
func NotEtcdMember(err error) bool {
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok {
		return false
	}
	for _, d := range ce.Details() {
		if v, err := connectproto.UnmarshalErrorDetail(d); err == nil {
			if _, ok := v.(*kubernetesv1.NotEtcdMember); ok {
				return true
			}
		}
	}
	return false
}

// EtcdRemoveMember removes the etcd member, a node's name or a member's ID in hex, through the
// control plane via. The control plane refuses when the voters left would have no healthy quorum,
// unless force. Only ctx bounds the call, not client.CallTimeout.
func EtcdRemoveMember(ctx context.Context, via *client.Conn, member string, force bool) (*kubernetesv1.EtcdRemoveMemberResponse, error) {
	return Client(via).EtcdRemoveMember(client.Unbounded(ctx), &kubernetesv1.EtcdRemoveMemberRequest{Member: member, Force: force})
}

// EtcdLeave takes the control plane over conn out of etcd. The node refuses when the voters left
// would have no healthy quorum; with force it leaves through the other members also when its own
// member does not answer. Only ctx bounds the call, not client.CallTimeout.
func EtcdLeave(ctx context.Context, conn *client.Conn, force bool) error {
	_, err := Client(conn).EtcdLeave(client.Unbounded(ctx), &kubernetesv1.EtcdLeaveRequest{Force: force})
	return err
}
