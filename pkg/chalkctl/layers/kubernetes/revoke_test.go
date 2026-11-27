package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
	kubernetesview "github.com/trevex/chalkos/pkg/statusview/layers/kubernetes"
)

func TestUnrevokedLine(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at    time.Time
		stale []string
		want  string
	}{
		{at, nil, "unrevoked node w1; the certificates it held before 2026-10-23T12:00:00Z are no longer trusted"},
		{at, []string{"os-ca"}, "unrevoked node w1; its certificates from before 2026-10-23T12:00:00Z work again until they expire: the os-ca was not rotated since\n" + reregister("w1")},
		{time.Time{}, []string{"os-ca", "kubernetes-ca"}, "unrevoked node w1; its certificates from before its revocation, at an unknown time, work again until they expire: the os-ca and kubernetes-ca were not rotated since\n" + reregister("w1")},
	} {
		if got := unrevokedLine("w1", tc.at, tc.stale); got != tc.want {
			t.Errorf("unrevokedLine(%v, %v) = %q, want %q", tc.at, tc.stale, got, tc.want)
		}
	}
}

// reregister is the line that tells how the old node registers its Node again.
func reregister(name string) string {
	return "the old " + name + "'s kubelet does not register its Node again by itself: reboot it with chalkctl node reboot " + name + " --no-drain" +
		", or restart its kubelet; a node installed afresh as " + name + " registers as usual"
}

// A refusal to revoke an etcd member says how to cut the control plane off instead; other errors
// pass unchanged.
func TestRevocationRefusal(t *testing.T) {
	err := revocationRefusal("cp1", fmt.Errorf("cp2: %w", &sdkkubernetes.EtcdMemberError{Node: "cp1"}))
	if err == nil || !strings.HasPrefix(err.Error(), "cp1 is still an etcd member; ") ||
		!strings.Contains(err.Error(), "chalkctl etcd remove-member cp1") || !strings.Contains(err.Error(), "chalkctl rotate os-ca") ||
		!strings.Contains(err.Error(), "pass --allow-control-plane") {
		t.Errorf("refusal: %v, want the remedy named", err)
	}
	other := errors.New("connection refused")
	if err := revocationRefusal("cp1", other); err != other {
		t.Errorf("other error: %v", err)
	}
	if err := revocationRefusal("cp1", nil); err != nil {
		t.Errorf("no error: %v", err)
	}
}

func TestEveryControlPlaneReadsRevoked(t *testing.T) {
	statuses := map[string]*kubernetesv1.StatusResponse{
		"read":     {State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED},
		"unread":   {State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, RevokedNodesError: "read the revoked nodes: connection refused"},
		"missing":  {State: "etcd data missing: restore etcd", Phase: kubernetesv1.Phase_PHASE_ETCD_DATA_MISSING},
		"waiting":  {State: "waiting for bootstrap", Phase: kubernetesv1.Phase_PHASE_WAITING},
		"left":     {State: "left etcd: reinstall the node", Phase: kubernetesv1.Phase_PHASE_LEFT},
		"no share": {State: "no share", Phase: kubernetesv1.Phase_PHASE_PREPARING},
	}
	status := func(_ context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		if st, ok := statuses[name]; ok {
			return st, nil
		}
		if name == "router" {
			// Wrapped, as chalkctl returns it.
			return nil, fmt.Errorf("%s: %w", name, sdkkubernetes.ErrNoKubernetes)
		}
		return nil, errors.New("connection refused")
	}
	for _, tc := range []struct {
		controlPlanes []string
		want          []string
	}{
		{[]string{"read", "missing", "waiting", "left", "no share", "router"}, nil},
		{[]string{"unread", "down"}, []string{"unread: cannot read the revoked nodes: read the revoked nodes: connection refused", "down: connection refused"}},
	} {
		err := everyControlPlaneReads(context.Background(), tc.controlPlanes, "w1", status)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%v: %v", tc.controlPlanes, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "pass --skip-read-check to revoke w1 anyway") {
			t.Errorf("%v: %v, want a refusal", tc.controlPlanes, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v: %v, want %q", tc.controlPlanes, err, w)
			}
		}
	}
}

// The line after a revocation warns while the revoked nodes fill more than 80% of their room.
func TestRevokedLine(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		resp *kubernetesv1.RevokeNodeResponse
		want string
	}{
		{&kubernetesv1.RevokeNodeResponse{RevokedAt: timestamppb.New(at), NodeDeleted: true, ListLength: 100, ListCapacity: 90_000},
			"revoked node w1 (since 2026-10-23T12:00:00Z); deleted its Node object"},
		{&kubernetesv1.RevokeNodeResponse{RevokedAt: timestamppb.New(at), ListLength: 72_000, ListCapacity: 90_000},
			"revoked node w1 (since 2026-10-23T12:00:00Z); it had no Node object to delete"},
		{&kubernetesv1.RevokeNodeResponse{RevokedAt: timestamppb.New(at), NodeDeleted: true, ListLength: 80_000, ListCapacity: 90_000},
			"revoked node w1 (since 2026-10-23T12:00:00Z); deleted its Node object\n" +
				"the revoked nodes fill 88% of their room; " + kubernetesview.MakeRevokedRoom},
	} {
		if got := revokedLine("w1", tc.resp); got != tc.want {
			t.Errorf("revokedLine(%v) = %q, want %q", tc.resp, got, tc.want)
		}
	}
}

// The lines after a prune name the nodes dropped and say how full the list is, with how to make
// room past 80%.
func TestPrunedLines(t *testing.T) {
	for _, tc := range []struct {
		resp *kubernetesv1.PruneRevokedResponse
		want string
	}{
		{&kubernetesv1.PruneRevokedResponse{ListLength: 2, ListCapacity: 90_000},
			"dropped no node from the revoked nodes\nthe revoked nodes fill 0% of their room"},
		{&kubernetesv1.PruneRevokedResponse{Pruned: []string{"w1", "w2"}, ListLength: 45_000, ListCapacity: 90_000},
			"dropped 2 nodes from the revoked nodes, which nothing lets back in: w1, w2\nthe revoked nodes fill 50% of their room"},
		{&kubernetesv1.PruneRevokedResponse{Pruned: []string{"w1"}, ListLength: 81_000, ListCapacity: 90_000},
			"dropped 1 node from the revoked nodes, which nothing lets back in: w1\nthe revoked nodes fill 90% of their room; " + kubernetesview.MakeRevokedRoom},
	} {
		if got := prunedLines(tc.resp); got != tc.want {
			t.Errorf("prunedLines(%v) = %q, want %q", tc.resp, got, tc.want)
		}
	}
}
