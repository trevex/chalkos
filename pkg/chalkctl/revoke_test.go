package chalkctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/internal/cli"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

// revokeNode is n1 as a bootstrapped control plane whose API server is a fake holding w1's Node.
func revokeNode(t *testing.T, ta *testApp) (string, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}})
	s := etcdNode(t, ta, func(o *kubernetesplugin.Options) {
		o.Hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return cs, nil }
	})
	// Its status reports it bootstrapped once its Kubernetes files are prepared.
	writeFile(t, kubernetesPaths(s).Prepared(), "")
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
		if err := ta.run(context.Background(), ta.args(append(args, "--via", "n1"), addr)); !errors.Is(err, cli.ErrUsage) {
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
	want := "unrevoked node w2; its certificates from before " + future.Format(time.RFC3339) + " work again until they expire: the os-ca and kubernetes-ca were not rotated since\nthe old w2's kubelet does not register its Node again by itself: reboot it with chalkctl reboot w2, " +
		"or restart its kubelet; a node installed afresh as w2 registers as usual\n"
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
	withKind(t, ta, k8s.KindWorker)
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

// installLab is the rotation lab whose inventory reaches the control planes where they serve,
// for installs of n1.
func installLab(t *testing.T) *rotationLab {
	t.Helper()
	l := newRotationLab(t)
	data, err := os.ReadFile(l.ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	inv := strings.NewReplacer("address: 10.0.0.11,", "address: "+l.addrs["cp1"]+",", "address: 10.0.0.12,", "address: "+l.addrs["cp2"]+",").Replace(string(data))
	writeFile(t, l.ta.inventory, inv)
	writeImageDir(t, filepath.Join(l.ta.dir, "images", "plain-metal"), "plain", "metal", testRootHash, false)
	return l
}

// install installs n1 in the lab, on a node in maintenance mode started for it.
func (l *rotationLab) install(args ...string) error {
	s := maintenanceNode()
	s.InPlace = func(context.Context, install.Request, []byte) error { return nil }
	addr := l.ta.startNode(l.t, s)
	return l.ta.run(context.Background(), append([]string{"install", "n1", "--insecure", "--inventory", l.ta.inventory, "--address", addr}, args...))
}

// install refuses a name a control plane lists as revoked, and installs while bootstrapped control
// planes cannot say which are revoked only with --force. A control plane that does not answer
// counts as one not installed yet, as while a cluster's nodes are installed one by one.
func TestInstallRefusesRevokedNames(t *testing.T) {
	l := installLab(t)
	l.revoked = []*kubernetesv1.RevokedNode{{Name: "n1", RevokedAt: timestamppb.New(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))}}
	err := l.install()
	if err == nil || !strings.Contains(err.Error(), "n1 is revoked since 2026-10-23T12:00:00Z") || !strings.Contains(err.Error(), "chalkctl node unrevoke n1") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("installing a revoked name: %v", err)
	}
	if err := l.install("--force"); err != nil {
		t.Errorf("--force: %v", err)
	}

	l.revoked = nil
	l.revokedErr = map[string]string{"cp1": "read the revoked nodes: connection refused"}
	l.unreported = map[string]bool{"cp2": true}
	if err := l.install(); err == nil || !strings.Contains(err.Error(), "no control-plane node said which nodes are revoked") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("installing without the list: %v", err)
	}

	l.revokedErr = nil
	l.unreported = nil
	if err := l.install(); err != nil {
		t.Errorf("installing a name that is not revoked: %v", err)
	}
	// The cluster's first nodes: one control plane is not installed yet, the other not bootstrapped.
	l.nodes["cp1"].down = true
	l.states = map[string]string{"cp2": "waiting for bootstrap"}
	if err := l.install(); err != nil {
		t.Errorf("installing before any control plane was bootstrapped: %v", err)
	}
}
