package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
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

// A control plane refuses to refresh the revocation of a node not revoked, the etcd member n1
// too, which NotRevoked tells; the lab's API server lists no revoked node.
func TestRefreshRevocation(t *testing.T) {
	t.Parallel()
	conn, _ := etcdMember(t)
	for _, name := range []string{"w1", "n1"} {
		if _, err := RefreshRevocation(context.Background(), conn, name); !NotRevoked(err) {
			t.Errorf("refreshing %s, not revoked: %v, want NotRevoked", name, err)
		}
	}
	if NotRevoked(nil) || NotRevoked(connect.NewError(connect.CodeNotFound, "local")) {
		t.Error("NotRevoked holds for another error than a node's NOT_FOUND")
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

// A full list is told by its reason, never by the message.
func TestRevokedListFull(t *testing.T) {
	t.Parallel()
	full := connect.NewError(connect.CodeFailedPrecondition, "revoking w1 would make the admission policy's map of the revoked nodes too long")
	if RevokedListFull(full) || RevokedListFull(nil) {
		t.Error("a refusal without the reason is RevokedListFull")
	}
	detail, err := connectproto.NewErrorDetail(&nodev1.ReasonDetail{Reason: nodev1.ErrorReason_ERROR_REASON_REVOKED_LIST_FULL})
	if err != nil {
		t.Fatal(err)
	}
	full = full.WithDetail(detail)
	if !RevokedListFull(fmt.Errorf("revoke w1: %w", full)) {
		t.Error("a refusal with the reason is not RevokedListFull")
	}
}

// A bootstrapped control plane prunes the revoked nodes and says how full their list is.
func TestPruneRevoked(t *testing.T) {
	t.Parallel()
	conn, _ := etcdMember(t)
	resp, err := PruneRevoked(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Pruned) != 0 || resp.ListCapacity != revoked.MaxLiteralLength {
		t.Errorf("pruned %q, capacity %d; want none of %d", resp.Pruned, resp.ListCapacity, revoked.MaxLiteralLength)
	}
}
