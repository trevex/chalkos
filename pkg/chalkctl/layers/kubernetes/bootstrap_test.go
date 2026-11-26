package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
)

// TestAdmitBootstrap refuses a bootstrap while another control plane of the inventory is an etcd
// member, is joining or does not answer, and lets --ignore-members past; workers, nodes without Kubernetes
// and control planes that wait or left etcd hold no cluster.
func TestAdmitBootstrap(t *testing.T) {
	statuses := map[string]*kubernetesv1.StatusResponse{
		"bootstrapped": {State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED},
		"joining":      {State: "joining the cluster at https://10.0.0.10:6443: etcd member 1 catches up", Phase: kubernetesv1.Phase_PHASE_JOINING},
		"missing":      {State: "etcd data missing: restore etcd or reinstall the node", Phase: kubernetesv1.Phase_PHASE_ETCD_DATA_MISSING},
		"waiting":      {State: "waiting for the cluster at https://10.0.0.10:6443", Phase: kubernetesv1.Phase_PHASE_WAITING},
		"left":         {State: "left etcd; reinstall the node to join the cluster again", Phase: kubernetesv1.Phase_PHASE_LEFT},
		"no share":     {State: "no share", Phase: kubernetesv1.Phase_PHASE_PREPARING},
	}
	status := func(_ context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		if st, ok := statuses[name]; ok {
			return st, nil
		}
		if name == "router" {
			return nil, fmt.Errorf("%s: %w", name, sdkkubernetes.ErrNoKubernetes)
		}
		return nil, errors.New("connection refused")
	}
	cluster := func(names ...string) fakeCluster {
		c := fakeCluster{types: map[string]string{"cp1": k8s.KindControlPlane, "w1": k8s.KindWorker}}
		for _, n := range names {
			c.types[n] = k8s.KindControlPlane
		}
		return c
	}
	for _, tc := range []struct {
		others []string
		want   []string
	}{
		{[]string{"waiting", "left", "no share", "router"}, nil},
		{[]string{"bootstrapped", "waiting"}, []string{"bootstrapped: bootstrapped"}},
		{[]string{"joining"}, []string{"joining: joining the cluster at"}},
		{[]string{"missing"}, []string{"missing: etcd data missing"}},
	} {
		c := cluster(tc.others...)
		err := admitBootstrap(context.Background(), c, "cp1", false, status, io.Discard)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%v: %v", tc.others, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "another control plane may hold the cluster") || !strings.Contains(err.Error(), "pass --ignore-members to bootstrap cp1 anyway") {
			t.Errorf("%v: %v, want a refusal", tc.others, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v: %v, want %q", tc.others, err, w)
			}
		}
		if err := admitBootstrap(context.Background(), c, "cp1", true, status, io.Discard); err != nil {
			t.Errorf("%v with --ignore-members: %v", tc.others, err)
		}
	}
}

func TestAdmitBootstrapWarnsOfSilentControlPlanes(t *testing.T) {
	c := fakeCluster{types: map[string]string{"cp1": k8s.KindControlPlane, "cp2": k8s.KindControlPlane}}
	status := func(context.Context, string) (*kubernetesv1.StatusResponse, error) {
		return nil, errors.New("connection refused")
	}
	var stderr strings.Builder
	if err := admitBootstrap(context.Background(), c, "cp1", false, status, &stderr); err != nil {
		t.Fatalf("admitBootstrap: %v", err)
	}
	if want := "warning: cp2 does not say whether it is a member of a cluster: connection refused\n"; stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}
}
