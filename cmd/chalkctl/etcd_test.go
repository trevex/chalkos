package main

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkd"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
)

// etcdNode is n1 as a bootstrapped control-plane node whose etcd member runs.
func etcdNode(t *testing.T, ta *testApp) *chalkd.Server {
	t.Helper()
	withKind(t, ta, manifest.KindControlPlane)
	s, _ := kubernetesNode(t, ta)
	k := s.Kubernetes
	share := kpki.ControlPlaneShare(ta.secrets.Kubernetes)
	data, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, k.Paths.Share(), string(data))
	writeFile(t, k.Paths.Cluster, `{"kind": "controlplane", "endpoint": "https://10.0.0.10:6443", "podCIDR": "10.244.0.0/16",
	  "serviceCIDR": "10.96.0.0/12", "dnsIP": "10.96.0.10", "domain": "cluster.local"}`)
	writeFile(t, k.Paths.NodeFile, `{"kubernetes": {"nodeName": "n1"}}`)
	writeFile(t, k.Paths.Bootstrapped(), "")
	k.LocalEtcd = etcdtest.StartNew(t, *share.EtcdCA, "n1").ClientURL
	// No API server lists the other members.
	k.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []net.IP) ([]string, error) {
		return nil, errors.New("connection refused")
	}
	return s
}

func TestEtcdMembers(t *testing.T) {
	ta := newTestApp(t)
	s := etcdNode(t, ta)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"etcd", "members", "--via", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(ta.stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "n1 ") ||
		!strings.Contains(lines[1], "https://127.0.0.1:") || !strings.HasSuffix(lines[1], "voter   healthy") {
		t.Errorf("members:\n%s", ta.stdout)
	}
	// --endpoint reaches one node, which --via names.
	if err := ta.run(context.Background(), ta.args([]string{"etcd", "members"}, addr)); err == nil || !strings.Contains(err.Error(), "--via") {
		t.Errorf("without --via: %v", err)
	}
}

func TestEtcdRemoveMemberRefusesOwnMember(t *testing.T) {
	ta := newTestApp(t)
	s := etcdNode(t, ta)
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"etcd", "remove-member", "n1", "--via", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "chalkctl etcd leave n1") {
		t.Errorf("err = %v, want the node's refusal naming chalkctl etcd leave", err)
	}
}

func TestEtcdLeaveRefusesLastVoter(t *testing.T) {
	ta := newTestApp(t)
	s := etcdNode(t, ta)
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"etcd", "leave", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "etcd's last voter") {
		t.Errorf("err = %v, want a refusal for the last voter", err)
	}
	if bootstrapped, _ := knode.Bootstrapped(s.Kubernetes.Paths); !bootstrapped {
		t.Error("a refused leave changed the node")
	}
}

func TestControlPlaneNodes(t *testing.T) {
	m := &manifest.Manifest{
		Roles: map[string]manifest.Role{"cp": {Kind: manifest.KindControlPlane}, "w": {Kind: manifest.KindWorker}},
		Nodes: map[string]manifest.Node{"cp3": {Role: "cp"}, "cp1": {Role: "cp"}, "w1": {Role: "w"}, "cp2": {Role: "cp"}},
	}
	if got := controlPlaneNodes(m, "cp2"); !slices.Equal(got, []string{"cp1", "cp3"}) {
		t.Errorf("controlPlaneNodes() = %v", got)
	}
}
