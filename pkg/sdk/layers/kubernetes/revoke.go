package kubernetes

import (
	"context"
	"fmt"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
)

// EtcdMemberError is RevokeNode's refusal of a node that is still an etcd member, a control
// plane: it holds the CA keys, so revoking it does not cut it off. Take it out of etcd first with
// EtcdLeave or EtcdRemoveMember, then rotate the CAs, or pass allowControlPlane.
type EtcdMemberError struct {
	// Node is the node's name.
	Node string
}

func (e *EtcdMemberError) Error() string {
	return fmt.Sprintf("%s is still an etcd member; revoking a control plane does not cut it off, because it holds the CA keys", e.Node)
}

// RevokeNode revokes the node by name through the control plane via: the cluster keeps it among
// the revoked nodes, whose node certificates no control plane renews, and its Node object is
// deleted. Unless allowControlPlane, it asks via for etcd's members first and refuses with an
// EtcdMemberError a node etcd lists. That check runs here, in the client: the control plane
// itself does not refuse to revoke an etcd member.
func RevokeNode(ctx context.Context, via *client.Conn, node string, allowControlPlane bool) (*kubernetesv1.RevokeNodeResponse, error) {
	if !allowControlPlane {
		m, err := Client(via).EtcdMembers(ctx, &kubernetesv1.EtcdMembersRequest{})
		if err != nil {
			return nil, err
		}
		if err := etcdMemberRefusal(node, m.Members); err != nil {
			return nil, err
		}
	}
	return Client(via).RevokeNode(ctx, &kubernetesv1.RevokeNodeRequest{Node: node})
}

// etcdMemberRefusal refuses to revoke a node that is still an etcd member, a control plane. A
// member that has not started has no name yet and is no node's.
func etcdMemberRefusal(name string, members []*kubernetesv1.EtcdMember) error {
	for _, m := range members {
		if m.Name != "" && m.Name == name {
			return &EtcdMemberError{Node: name}
		}
	}
	return nil
}

// RevokedListFull reports whether err is a control plane's refusal of RevokeNode because the
// revoked nodes' list has no room, by the reason REVOKED_LIST_FULL. Rotating the OS CA and the
// Kubernetes CA makes room: chalkctl rotate --finish then calls PruneRevoked, which drops the nodes
// revoked before both.
func RevokedListFull(err error) bool {
	return client.Reason(err) == nodev1.ErrorReason_ERROR_REASON_REVOKED_LIST_FULL
}

// PruneRevoked has the control plane via drop the revoked nodes nothing lets back in: those whose
// certificates have all expired, and those revoked before every OS CA and Kubernetes CA it trusts
// was made. Call it once every node confirmed the finish of a rotation of the OS CA or the
// Kubernetes CA; before that, a control plane that has not finished the rotation may still trust
// an old CA a dropped node's certificates chain to. Control planes drop expired nodes by
// themselves.
func PruneRevoked(ctx context.Context, via *client.Conn) (*kubernetesv1.PruneRevokedResponse, error) {
	return Client(via).PruneRevoked(ctx, &kubernetesv1.PruneRevokedRequest{})
}
