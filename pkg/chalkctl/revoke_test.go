package chalkctl

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
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
	want := "unrevoked node w2; its certificates from before " + future.Format(time.RFC3339) + " work again until they expire: the os-ca and kubernetes-ca were not rotated since\n"
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
		{at, []string{"os-ca"}, "unrevoked node w1; its certificates from before 2026-10-23T12:00:00Z work again until they expire: the os-ca was not rotated since"},
		{time.Time{}, []string{"os-ca", "kubernetes-ca"}, "unrevoked node w1; its certificates from before its revocation, at an unknown time, work again until they expire: the os-ca and kubernetes-ca were not rotated since"},
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
