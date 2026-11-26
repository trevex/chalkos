package kubernetes

import (
	"context"
	"errors"
	"testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
)

// A node that etcd lists as a member is revoked only when the caller allows a control plane; a
// node etcd does not list is revoked.
func TestRevokeNodeRefusesAnEtcdMember(t *testing.T) {
	t.Parallel()
	conn, _ := etcdMember(t)
	_, err := RevokeNode(context.Background(), conn, "n1", false)
	if e, ok := errors.AsType[*EtcdMemberError](err); !ok || e.Node != "n1" {
		t.Errorf("revoking the etcd member n1: %v", err)
	}
	if resp, err := RevokeNode(context.Background(), conn, "w1", false); err != nil || resp.GetRevokedAt() == nil {
		t.Errorf("revoking w1, no etcd member: %v, %v", resp, err)
	}
	if resp, err := RevokeNode(context.Background(), conn, "n1", true); err != nil || resp.GetRevokedAt() == nil {
		t.Errorf("revoking n1 with allowControlPlane: %v, %v", resp, err)
	}
}

// A node etcd lists by name, a voter or a learner, is refused; an empty name matches no member
// that has not started.
func TestEtcdMemberRefusal(t *testing.T) {
	members := []*kubernetesv1.EtcdMember{{Name: "cp1"}, {Name: "cp2", Learner: true}, {}}
	for name, want := range map[string]bool{"cp1": true, "cp2": true, "w1": false, "": false} {
		err := etcdMemberRefusal(name, members)
		if (err != nil) != want {
			t.Errorf("%q: %v, want refused %v", name, err, want)
		}
		if e, ok := errors.AsType[*EtcdMemberError](err); err != nil && (!ok || e.Node != name) {
			t.Errorf("%q: %v, want an EtcdMemberError for it", name, err)
		}
	}
}
