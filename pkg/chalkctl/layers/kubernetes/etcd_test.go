package kubernetes

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
)

// chalkctl moves on to the next control plane when one is unavailable or is no etcd member, and
// stops at any other answer.
func TestTryNextControlPlane(t *testing.T) {
	notMember := connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is not an etcd member; ask another control-plane node"))
	detail, err := connect.NewErrorDetail(&kubernetesv1.NotEtcdMember{})
	if err != nil {
		t.Fatal(err)
	}
	notMember.AddDetail(detail)
	for _, tc := range []struct {
		err  error
		next bool
	}{
		{connect.NewError(connect.CodeUnavailable, errors.New("connection refused")), true},
		{connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout")), true},
		{notMember, true},
		// The message alone does not tell chalkctl to move on.
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is not an etcd member; ask another control-plane node")), false},
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("removing cp2 would leave etcd without a healthy quorum; pass --force to remove it anyway")), false},
		{connect.NewError(connect.CodeNotFound, errors.New("etcd has no member cp9")), false},
		{errors.New("certificate mismatch"), false},
	} {
		if got := tryNextControlPlane(tc.err); got != tc.next {
			t.Errorf("tryNextControlPlane(%v) = %v, want %v", tc.err, got, tc.next)
		}
	}
}
