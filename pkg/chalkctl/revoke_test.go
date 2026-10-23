package chalkctl

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/identity"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

// revokeNode is n1 as a bootstrapped control plane whose API server is a fake holding w1's Node.
func revokeNode(t *testing.T, ta *testApp) (string, *fake.Clientset) {
	t.Helper()
	s := etcdNode(t, ta)
	// Its status reports it bootstrapped once its Kubernetes files are prepared.
	writeFile(t, s.Kubernetes.Paths.Prepared(), "")
	cs := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}})
	s.Kubernetes.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return cs, nil }
	return ta.startNode(t, s), cs
}

func TestNodeRevoke(t *testing.T) {
	ta := newTestApp(t)
	addr, cs := revokeNode(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"node", "revoke", "w1", "--via", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	list, err := revoked.Load(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	since := list["w1"].Format(time.RFC3339)
	if got, want := ta.stdout.String(), "revoked node w1 (since "+since+"); deleted its Node object\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	ta.stdout.Reset()
	if err := ta.run(context.Background(), ta.args([]string{"node", "revoke", "w1", "--via", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if got, want := ta.stdout.String(), "revoked node w1 (since "+since+"); it had no Node object to delete\n"; got != want {
		t.Errorf("revoking again: stdout = %q, want %q", got, want)
	}
	// The node refuses a name no Node can have.
	err = ta.run(context.Background(), ta.args([]string{"node", "revoke", "W1", "--via", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), `the node name "W1" is invalid`) {
		t.Errorf("revoking W1: %v, want the node's refusal", err)
	}
	for _, args := range [][]string{{"node", "revoke"}, {"node", "revoke", "w1", "w2"}, {"node", "unrevoke"}} {
		if err := ta.run(context.Background(), ta.args(append(args, "--via", "n1"), addr)); err == nil || !strings.Contains(err.Error(), "usage: chalkctl node") {
			t.Errorf("%v: %v, want the usage", args, err)
		}
	}
	// --address reaches one node, which --via names.
	if err := ta.run(context.Background(), ta.args([]string{"node", "revoke", "w1"}, addr)); err == nil || !strings.Contains(err.Error(), "--via") {
		t.Errorf("without --via: %v", err)
	}
}

func TestNodeUnrevoke(t *testing.T) {
	ta := newTestApp(t)
	addr, cs := revokeNode(t, ta)
	ctx := context.Background()
	// The cluster's CAs were made now: after w1's revocation, before w2's.
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	for name, at := range map[string]time.Time{"w1": old, "w2": future} {
		if _, err := revoked.Revoke(ctx, cs, name, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := ta.run(ctx, ta.args([]string{"node", "unrevoke", "w1", "--via", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if got, want := ta.stdout.String(), "unrevoked node w1; the certificates it held before 2020-01-02T03:04:05Z are no longer trusted\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}

	ta.stdout.Reset()
	err := ta.run(ctx, ta.args([]string{"node", "unrevoke", "w2", "--via", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "chalkctl rotate os-ca") || !strings.Contains(err.Error(), "chalkctl rotate kubernetes-ca") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("unrevoking w2: %v, want the node's refusal naming the rotations", err)
	}
	if list, _ := revoked.Load(ctx, cs); !list["w2"].Equal(future) {
		t.Errorf("the refusal changed the revoked nodes: %v", list)
	}
	if err := ta.run(ctx, ta.args([]string{"node", "unrevoke", "w2", "--via", "n1", "--force"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "unrevoked node w2; its certificates from before " + future.Format(time.RFC3339) + " work again until they expire: the os-ca and kubernetes-ca were not rotated since\n" + reregister("w2") + "\n"
	if got := ta.stdout.String(); got != want {
		t.Errorf("forced: stdout = %q, want %q", got, want)
	}
	if list, _ := revoked.Load(ctx, cs); len(list) != 0 {
		t.Errorf("revoked nodes left: %v", list)
	}

	err = ta.run(ctx, ta.args([]string{"node", "unrevoke", "w1", "--via", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "node w1 is not revoked") {
		t.Errorf("unrevoking w1 again: %v", err)
	}
}

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

// status on a control plane lists the revoked nodes with their times.
func TestStatusListsRevokedNodes(t *testing.T) {
	ta := newTestApp(t)
	addr, cs := revokeNode(t, ta)
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if _, err := revoked.Revoke(context.Background(), cs, "w1", at); err != nil {
		t.Fatal(err)
	}
	cm, _ := cs.CoreV1().ConfigMaps(revoked.Namespace).Get(context.Background(), revoked.Name, metav1.GetOptions{})
	cm.Data["w2"] = "garbled"
	if _, err := cs.CoreV1().ConfigMaps(revoked.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	out := ta.stdout.String()
	if !strings.Contains(out, "revoked nodes:\n  w1  since 2026-10-23T12:00:00Z\n  w2  at an unknown time\n") {
		t.Errorf("status:\n%s", out)
	}
}

func TestRevokedLines(t *testing.T) {
	if got := revokedLines(&nodev1.KubernetesStatus{Kind: "worker"}); got != nil {
		t.Errorf("a worker's lines = %q", got)
	}
	got := revokedLines(&nodev1.KubernetesStatus{RevokedNodesError: "read the revoked nodes: connection refused"})
	if want := []string{"revoked nodes unknown: read the revoked nodes: connection refused"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// node renew and apply-config --kubernetes-share issue no certificate to a node a control plane
// lists as revoked, nor while no control plane says which nodes are, unless forced.
func TestCertificatesRefusedToRevokedNodes(t *testing.T) {
	ta := newTestApp(t)
	addr, cs := revokeNode(t, ta)
	ctx := context.Background()
	if _, err := revoked.Revoke(ctx, cs, "n1", time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{{"node", "renew", "n1"}, {"apply-config", "n1", "--kubernetes-share"}} {
		err := ta.run(ctx, ta.args(cmd, addr))
		if err == nil || !strings.Contains(err.Error(), "n1 is revoked since 2026-10-23T12:00:00Z") ||
			!strings.Contains(err.Error(), "chalkctl node unrevoke n1") || !strings.Contains(err.Error(), "--force") {
			t.Errorf("%v: %v, want refused as revoked", cmd, err)
		}
	}
	if err := ta.run(ctx, ta.args([]string{"node", "renew", "n1", "--force"}, addr)); err != nil {
		t.Errorf("node renew --force: %v", err)
	}
	// apply-config without a share delivers no certificate.
	if err := ta.run(ctx, ta.args([]string{"apply-config", "n1", "--diff"}, addr)); err != nil {
		t.Errorf("apply-config --diff: %v", err)
	}
	if err := ta.run(ctx, ta.args([]string{"apply-config", "n1", "--force"}, addr)); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("apply-config --force without --kubernetes-share: %v", err)
	}
	if err := ta.run(ctx, ta.args([]string{"apply-config", "n1", "--kubernetes-share", "--force"}, addr)); err != nil && strings.Contains(err.Error(), "revoked") {
		t.Errorf("apply-config --kubernetes-share --force: %v", err)
	}

	if _, _, err := revoked.Unrevoke(ctx, cs, "n1", nil); err != nil {
		t.Fatal(err)
	}
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	err := ta.run(ctx, ta.args([]string{"node", "renew", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "no control-plane node said which nodes are revoked") || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("node renew without the list: %v", err)
	}
}

// node renew renews a node of a cluster without control planes, which keeps no revoked nodes.
func TestNodeRenewWithoutControlPlanes(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, identity.KindWorker)
	s, _ := kubernetesNode(t, ta)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"node", "renew", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
}

// Revoking a control plane does not cut it off: node revoke refuses the control plane it asks,
// unless forced.
func TestNodeRevokeRefusesAControlPlane(t *testing.T) {
	ta := newTestApp(t)
	addr, cs := revokeNode(t, ta)
	ctx := context.Background()
	err := ta.run(ctx, ta.args([]string{"node", "revoke", "n1", "--via", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "CA keys") || !strings.Contains(err.Error(), "chalkctl etcd leave n1") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("revoking the control plane asked: %v", err)
	}
	if list, _ := revoked.Load(ctx, cs); len(list) != 0 {
		t.Errorf("the refusal revoked %v", list)
	}
	if err := ta.run(ctx, ta.args([]string{"node", "revoke", "n1", "--via", "n1", "--force"}, addr)); err != nil {
		t.Fatal(err)
	}
	if list, _ := revoked.Load(ctx, cs); len(list) != 1 {
		t.Errorf("revoked nodes %v, want n1", list)
	}
}

func TestEtcdMemberRefusal(t *testing.T) {
	members := []*nodev1.EtcdMember{{Name: "cp1"}, {Name: "cp2", Learner: true}, {}}
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

// A cluster without control planes keeps no revoked nodes.
func TestNodeRevokeWithoutControlPlanes(t *testing.T) {
	ta := newTestApp(t)
	for _, cmd := range []string{"revoke", "unrevoke"} {
		err := ta.run(context.Background(), []string{"node", cmd, "w1", "--inventory", ta.inventory})
		if err == nil || !strings.Contains(err.Error(), "keeps no revoked nodes") || !strings.Contains(err.Error(), "chalkctl rotate os-ca") {
			t.Errorf("node %s: %v", cmd, err)
		}
	}
}

// reregister is the line that tells how the old node registers its Node again.
func reregister(name string) string {
	return "the old " + name + "'s kubelet does not register its Node again by itself: reboot it with chalkctl reboot " + name +
		", or restart its kubelet; a node installed afresh as " + name + " registers as usual"
}

// node revoke refuses while a control plane may renew the node's certificate unaware of the
// revocation: one that cannot say which nodes are revoked, unless forced.
func TestNodeRevokeRefusesUnlessEveryControlPlaneReads(t *testing.T) {
	ta := newTestApp(t)
	addr, cs := revokeNode(t, ta)
	ctx := context.Background()
	reads := 0
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		reads++
		return reads == 1, nil, errors.New("connection refused")
	})
	err := ta.run(ctx, ta.args([]string{"node", "revoke", "w1", "--via", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "n1: cannot read the revoked nodes") || !strings.Contains(err.Error(), "connection refused") ||
		!strings.Contains(err.Error(), "--force") {
		t.Errorf("revoking w1: %v, want a refusal naming n1", err)
	}
	if list, _ := revoked.Load(ctx, cs); len(list) != 0 {
		t.Errorf("the refusal revoked %v", list)
	}
	if err := ta.run(ctx, ta.args([]string{"node", "revoke", "w1", "--via", "n1", "--force"}, addr)); err != nil {
		t.Fatal(err)
	}
	if list, _ := revoked.Load(ctx, cs); len(list) != 1 {
		t.Errorf("revoked nodes %v, want w1", list)
	}
}

func TestEveryControlPlaneReadsRevoked(t *testing.T) {
	statuses := map[string]*nodev1.StatusResponse{
		"read":     {Kubernetes: &nodev1.KubernetesStatus{State: "bootstrapped", RevokedNodesRead: true}},
		"old":      {Kubernetes: &nodev1.KubernetesStatus{State: "bootstrapped"}},
		"unread":   {Kubernetes: &nodev1.KubernetesStatus{State: "etcd data missing: restore etcd", RevokedNodesError: "read the revoked nodes: connection refused"}},
		"waiting":  {Kubernetes: &nodev1.KubernetesStatus{State: "waiting for bootstrap"}},
		"left":     {Kubernetes: &nodev1.KubernetesStatus{State: "left etcd: reinstall the node"}},
		"no share": {Kubernetes: &nodev1.KubernetesStatus{State: "no share"}},
	}
	status := func(_ context.Context, name string) (*nodev1.StatusResponse, error) {
		if st, ok := statuses[name]; ok {
			return st, nil
		}
		return nil, errors.New("connection refused")
	}
	for _, tc := range []struct {
		controlPlanes []string
		want          []string
	}{
		{[]string{"read", "waiting", "left", "no share"}, nil},
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
