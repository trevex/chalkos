package kubernetes

import (
	"context"
	"fmt"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
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
