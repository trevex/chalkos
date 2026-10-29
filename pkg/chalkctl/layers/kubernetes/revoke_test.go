package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
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
	return "the old " + name + "'s kubelet does not register its Node again by itself: reboot it with chalkctl reboot " + name +
		", or restart its kubelet; a node installed afresh as " + name + " registers as usual"
}

func TestEtcdMemberRefusal(t *testing.T) {
	members := []*kubernetesv1.EtcdMember{{Name: "cp1"}, {Name: "cp2", Learner: true}, {}}
	for name, want := range map[string]bool{"cp1": true, "cp2": true, "w1": false, "": false} {
		err := etcdMemberRefusal(name, members)
		if (err != nil) != want {
			t.Errorf("%q: %v, want refused %v", name, err, want)
		}
		if err != nil && (!strings.Contains(err.Error(), "chalkctl etcd remove-member "+name) || !strings.Contains(err.Error(), "chalkctl rotate os-ca")) {
			t.Errorf("%q: %v, want the remedy named", name, err)
		}
	}
}

func TestEveryControlPlaneReadsRevoked(t *testing.T) {
	statuses := map[string]*kubernetesv1.StatusResponse{
		"read":     {State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, RevokedNodesRead: true},
		"old":      {State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED},
		"unread":   {State: "etcd data missing: restore etcd", Phase: kubernetesv1.Phase_PHASE_ETCD_DATA_MISSING, RevokedNodesError: "read the revoked nodes: connection refused"},
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
			return nil, fmt.Errorf("%s: %w", name, errNoKubernetes)
		}
		return nil, errors.New("connection refused")
	}
	for _, tc := range []struct {
		controlPlanes []string
		want          []string
	}{
		{[]string{"read", "waiting", "left", "no share", "router"}, nil},
		{[]string{"read", "old"}, []string{"old: its chalkd predates node revocation and renews w1's node certificate until it is upgraded"}},
		{[]string{"unread", "down"}, []string{"unread: cannot read the revoked nodes: read the revoked nodes: connection refused", "down: connection refused"}},
	} {
		err := everyControlPlaneReads(context.Background(), tc.controlPlanes, "w1", status)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%v: %v", tc.controlPlanes, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "pass --force to revoke w1 anyway") {
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
