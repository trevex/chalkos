package chalkd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/addons"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
	"github.com/trevex/chalkos/pkg/pki"
)

// revokeLab is a bootstrapped control plane whose API server is a fake holding w1's Node.
type revokeLab struct {
	s  *Server
	cs *fake.Clientset
}

func newRevokeLab(t *testing.T) *revokeLab {
	t.Helper()
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	write(t, s.Kubernetes.Paths.Bootstrapped(), "")
	l := &revokeLab{s: s, cs: fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}})}
	s.Kubernetes.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return l.cs, nil }
	return l
}

// trustOSCAs makes the node trust, besides the OS CA that issued its certificate, an OS CA made at.
func (l *revokeLab) trustOSCAs(t *testing.T, at time.Time) {
	t.Helper()
	own, err := testOSCA()
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.NewOSCA(at)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.s.Paths.StateDir, "chalkd")
	write(t, filepath.Join(dir, CAFile), pki.Bundle(own.Certificate, other.Certificate))
	if l.s.Certificate, err = LoadNodeCertificate(dir); err != nil {
		t.Fatal(err)
	}
}

// kubernetesCAs gives the node a share whose Kubernetes CA was made at, and which accepts a
// Kubernetes CA made at each of accepted.
func (l *revokeLab) kubernetesCAs(t *testing.T, at time.Time, accepted ...time.Time) {
	t.Helper()
	k, err := pki.NewKubernetesSecrets(at)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accepted {
		old, err := pki.NewCA("chalkos Kubernetes CA", a)
		if err != nil {
			t.Fatal(err)
		}
		k.Accepted.CA = append(k.Accepted.CA, old.Certificate)
	}
	data, err := kpki.ControlPlaneShare(k, testNodeCA(t)).Encode()
	if err != nil {
		t.Fatal(err)
	}
	write(t, l.s.Kubernetes.Paths.Share(), string(data))
}

func (l *revokeLab) list(t *testing.T) revoked.List {
	t.Helper()
	list, err := revoked.Load(context.Background(), l.cs)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func (l *revokeLab) revoke(name string) (*nodev1.RevokeNodeResponse, error) {
	resp, err := l.s.RevokeNode(context.Background(), connect.NewRequest(&nodev1.RevokeNodeRequest{Node: name}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (l *revokeLab) unrevoke(name string, force bool) (*nodev1.UnrevokeNodeResponse, error) {
	resp, err := l.s.UnrevokeNode(context.Background(), connect.NewRequest(&nodev1.UnrevokeNodeRequest{Node: name, Force: force}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// TestRevokeNode lists w1 and deletes its Node; revoking it again keeps the first time.
func TestRevokeNode(t *testing.T) {
	l := newRevokeLab(t)
	before := time.Now().Add(-time.Second)
	got, err := l.revoke("w1")
	if err != nil {
		t.Fatal(err)
	}
	at := got.RevokedAt.AsTime()
	if !got.NodeDeleted || at.Before(before) || at.After(time.Now()) {
		t.Errorf("revoked at %v, Node deleted %v; want now and deleted", at, got.NodeDeleted)
	}
	if _, err := l.cs.CoreV1().Nodes().Get(context.Background(), "w1", metav1.GetOptions{}); err == nil {
		t.Error("w1's Node is still there")
	}
	if list := l.list(t); !list["w1"].Equal(at) {
		t.Errorf("revoked nodes %v, want w1 at %v", list, at)
	}
	again, err := l.revoke("w1")
	if err != nil {
		t.Fatal(err)
	}
	if again.NodeDeleted || !again.RevokedAt.AsTime().Equal(at) {
		t.Errorf("revoking again: at %v, Node deleted %v; want %v and nothing deleted", again.RevokedAt.AsTime(), again.NodeDeleted, at)
	}
	for _, name := range []string{"", "W1", "w_1", "w1/x"} {
		if _, err := l.revoke(name); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("revoking %q: %v, want InvalidArgument", name, err)
		}
		if _, err := l.unrevoke(name, true); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("unrevoking %q: %v, want InvalidArgument", name, err)
		}
	}
}

// TestRevokeNodeOnControlPlanesOnly refuses both calls where no list is kept.
func TestRevokeNodeOnControlPlanesOnly(t *testing.T) {
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	unbootstrapped, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	plain, _ := installedServer(t, section("", ""), false)
	for name, tc := range map[string]struct {
		s    *Server
		want string
	}{
		"a worker":                  {worker, "ask a control-plane node"},
		"an unbootstrapped control": {unbootstrapped, "not an etcd member"},
		"a node without Kubernetes": {plain, "chalkctl rotate os-ca"},
	} {
		_, err := tc.s.RevokeNode(context.Background(), connect.NewRequest(&nodev1.RevokeNodeRequest{Node: "w1"}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("revoke on %s: %v, want a refusal naming %q", name, err, tc.want)
		}
		_, err = tc.s.UnrevokeNode(context.Background(), connect.NewRequest(&nodev1.UnrevokeNodeRequest{Node: "w1"}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("unrevoke on %s: %v, want FailedPrecondition", name, err)
		}
	}
}

// TestUnrevokeNode unrevokes a node at once only when the OS CAs and the Kubernetes CAs the node
// trusts were all made after the revocation; otherwise only when forced.
func TestUnrevokeNode(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct {
		revokedAt time.Time
		// oldOSCA and oldKubernetesCA make the CA trusted besides the current ones, or the
		// Kubernetes CA itself, three days ago.
		oldOSCA, oldKubernetesCA, acceptedKubernetesCA bool
		stale                                          []string
	}{
		"CAs made after the revocation": {revokedAt: now.Add(-2 * time.Hour)},
		// CAs are backdated an hour, which does not make them older than the revocation.
		"CAs made right after the revocation":   {revokedAt: now.Add(-30 * time.Minute)},
		"an OS CA from before":                  {revokedAt: now.Add(-48 * time.Hour), oldOSCA: true, stale: []string{"os-ca"}},
		"a Kubernetes CA from before":           {revokedAt: now.Add(-48 * time.Hour), oldKubernetesCA: true, stale: []string{"kubernetes-ca"}},
		"an accepted Kubernetes CA from before": {revokedAt: now.Add(-48 * time.Hour), acceptedKubernetesCA: true, stale: []string{"kubernetes-ca"}},
		"both from before":                      {revokedAt: now.Add(-48 * time.Hour), oldOSCA: true, oldKubernetesCA: true, stale: []string{"os-ca", "kubernetes-ca"}},
		"CAs made before the revocation":        {revokedAt: now.Add(2 * time.Hour), stale: []string{"os-ca", "kubernetes-ca"}},
	} {
		t.Run(name, func(t *testing.T) {
			l := newRevokeLab(t)
			old := now.Add(-72 * time.Hour)
			if tc.oldOSCA {
				l.trustOSCAs(t, old)
			}
			switch {
			case tc.oldKubernetesCA:
				l.kubernetesCAs(t, old)
			case tc.acceptedKubernetesCA:
				l.kubernetesCAs(t, now, old)
			}
			if _, err := revoked.Revoke(context.Background(), l.cs, "w1", tc.revokedAt); err != nil {
				t.Fatal(err)
			}
			at := tc.revokedAt.UTC().Truncate(time.Second)
			if len(tc.stale) > 0 {
				_, err := l.unrevoke("w1", false)
				if connect.CodeOf(err) != connect.CodeFailedPrecondition {
					t.Fatalf("unrevoke: %v, want FailedPrecondition", err)
				}
				for _, ca := range []string{"os-ca", "kubernetes-ca"} {
					if named := strings.Contains(err.Error(), "chalkctl rotate "+ca); named != slices.Contains(tc.stale, ca) {
						t.Errorf("the refusal %q names %s: %v", err, ca, named)
					}
				}
				if _, ok := l.list(t)["w1"]; !ok {
					t.Fatal("the refusal unrevoked w1")
				}
			}
			got, err := l.unrevoke("w1", len(tc.stale) > 0)
			if err != nil {
				t.Fatal(err)
			}
			if !got.RevokedAt.AsTime().Equal(at) || !slices.Equal(got.StaleCas, tc.stale) {
				t.Errorf("revoked at %v, stale CAs %q; want %v, %q", got.RevokedAt.AsTime(), got.StaleCas, at, tc.stale)
			}
			if list := l.list(t); len(list) != 0 {
				t.Errorf("revoked nodes %v after the unrevoke, want none", list)
			}
		})
	}
}

func TestUnrevokeNodeNotRevoked(t *testing.T) {
	l := newRevokeLab(t)
	if _, err := l.unrevoke("w1", true); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unrevoking w1: %v, want NotFound", err)
	}
}

// TestUnrevokeNodeRevokedAtAnUnknownTime counts every CA as made before a revocation whose time
// cannot be read.
func TestUnrevokeNodeRevokedAtAnUnknownTime(t *testing.T) {
	l := newRevokeLab(t)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: revoked.Namespace, Name: revoked.Name}, Data: map[string]string{"w1": "yesterday"}}
	if _, err := l.cs.CoreV1().ConfigMaps(revoked.Namespace).Create(context.Background(), cm, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := l.unrevoke("w1", false)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "chalkctl rotate os-ca") || !strings.Contains(err.Error(), "chalkctl rotate kubernetes-ca") {
		t.Fatalf("unrevoke: %v, want a refusal naming both CAs", err)
	}
	got, err := l.unrevoke("w1", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.RevokedAt != nil || !slices.Equal(got.StaleCas, []string{"os-ca", "kubernetes-ca"}) {
		t.Errorf("revoked at %v, stale CAs %q; want no time and both CAs", got.RevokedAt, got.StaleCas)
	}
}

// TestStatusListsRevokedNodes lists the revoked nodes in a bootstrapped control plane's status,
// and why they could not be read in place of failing the status.
func TestStatusListsRevokedNodes(t *testing.T) {
	l := newRevokeLab(t)
	if _, err := l.revoke("w2"); err != nil {
		t.Fatal(err)
	}
	cm, err := l.cs.CoreV1().ConfigMaps(revoked.Namespace).Get(context.Background(), revoked.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["w1"] = "yesterday"
	if _, err := l.cs.CoreV1().ConfigMaps(revoked.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := l.s.Kubernetes.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := st.RevokedNodes
	if len(got) != 2 || got[0].Name != "w1" || got[0].RevokedAt != nil || got[1].Name != "w2" || got[1].RevokedAt.AsTime().Format(revoked.TimeFormat) != cm.Data["w2"] || st.RevokedNodesError != "" || !st.RevokedNodesRead {
		t.Errorf("revoked nodes %v, error %q; want w1 at an unknown time and w2 at %s", got, st.RevokedNodesError, cm.Data["w2"])
	}

	l.s.Kubernetes.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
		return nil, errors.New("connection refused")
	}
	if st, err = l.s.Kubernetes.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.RevokedNodes) != 0 || st.RevokedNodesRead || !strings.Contains(st.RevokedNodesError, "connection refused") || !strings.HasPrefix(st.State, "bootstrapped") {
		t.Errorf("without the API server: revoked nodes %v, error %q, state %q", st.RevokedNodes, st.RevokedNodesError, st.State)
	}

	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	if st, err := worker.Kubernetes.status(context.Background()); err != nil || st.RevokedNodes != nil || st.RevokedNodesError != "" || st.RevokedNodesRead {
		t.Errorf("a worker's status: %v, %v", st, err)
	}
}

// policyNames is the map of revoked nodes the applied admission policy names.
func (l *revokeLab) policyNames(t *testing.T) string {
	t.Helper()
	p, err := l.cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(context.Background(), revoked.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p.Spec.Variables[0].Expression
}

// TestRevokeNodeAppliesThePolicy applies the admission policy with the nodes listed after each
// revoke and unrevoke. Where it cannot, the call fails, saying the list changed and that calling
// again applies the policy.
func TestRevokeNodeAppliesThePolicy(t *testing.T) {
	l := newRevokeLab(t)
	resp, err := l.revoke("w1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.policyNames(t), `{"w1": "since `+resp.RevokedAt.AsTime().UTC().Format(revoked.TimeFormat)+`"}`; got != want {
		t.Errorf("after the revoke the policy names %s, want %s", got, want)
	}
	if _, err := l.unrevoke("w1", true); err != nil {
		t.Fatal(err)
	}
	if got := l.policyNames(t); got != "{}" {
		t.Errorf("after the unrevoke the policy names %s", got)
	}

	failing := true
	l.cs.PrependReactor("patch", "validatingadmissionpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failing {
			return true, nil, errors.New("connection reset")
		}
		return false, nil, nil
	})
	_, err = l.revoke("w2")
	if err == nil || !strings.Contains(err.Error(), "connection reset") || !strings.Contains(err.Error(), "w2 is revoked") || !strings.Contains(err.Error(), "revoking it again") {
		t.Errorf("revoke with the policy failing: %v", err)
	}
	if _, ok := l.list(t)["w2"]; !ok {
		t.Error("w2 is not listed")
	}
	failing = false
	if _, err := l.revoke("w2"); err != nil {
		t.Fatal(err)
	}
	if got := l.policyNames(t); !strings.Contains(got, `"w2"`) {
		t.Errorf("revoking again: the policy names %s", got)
	}

	failing = true
	_, err = l.unrevoke("w2", true)
	if err == nil || !strings.Contains(err.Error(), "connection reset") || !strings.Contains(err.Error(), "w2 is unrevoked") || !strings.Contains(err.Error(), "unrevoking it again") {
		t.Errorf("unrevoke with the policy failing: %v", err)
	}
	failing = false
	if _, err := l.unrevoke("w2", true); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unrevoking again: %v, want NotFound", err)
	}
	if got := l.policyNames(t); got != "{}" {
		t.Errorf("unrevoking again: the policy names %s", got)
	}
}

// TestStatusReadsTheNodeWhileTheListHangs bounds the read of the revoked nodes and that of the
// Node apart, so a read that hangs leaves the other its time.
func TestStatusReadsTheNodeWhileTheListHangs(t *testing.T) {
	l := newRevokeLab(t)
	k := l.s.Kubernetes
	k.StatusTimeout = 200 * time.Millisecond
	l.cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		time.Sleep(k.StatusTimeout)
		return true, nil, context.DeadlineExceeded
	})
	k.Node = func(ctx context.Context) (*corev1.Node, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return readyNode(), nil
	}
	st, err := k.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.NodeReady != "True" || st.RevokedNodesError == "" {
		t.Errorf("node ready %q, revoked nodes error %q; want the Node read and the list's error", st.NodeReady, st.RevokedNodesError)
	}
}

// A revocation that would make the admission policy's map of revoked nodes longer than the API
// server compiles is refused before the node is listed; one already listed is revoked again.
func TestRevokeNodeRefusesPastThePolicysLimit(t *testing.T) {
	l := newRevokeLab(t)
	data := map[string]string{}
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).Format(revoked.TimeFormat)
	for i := 0; addons.RevokedLength(asList(data)) <= addons.MaxRevokedLength-200; i++ {
		data[fmt.Sprintf("node-%05d-%s", i, strings.Repeat("x", 40))] = at
	}
	if _, err := l.cs.CoreV1().ConfigMaps(revoked.Namespace).Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: revoked.Namespace, Name: revoked.Name},
		Data:       data,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// A name of 246 characters, whose entry alone takes the room left.
	long := "w1." + strings.Repeat(strings.Repeat("x", 59)+".", 4) + "end"
	_, err := l.revoke(long)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "chalkctl rotate os-ca") || !strings.Contains(err.Error(), "chalkctl node unrevoke") {
		t.Errorf("revoking %s: %v, want refused naming the rotations and unrevoke", long, err)
	}
	if _, ok := l.list(t)[long]; ok {
		t.Errorf("the refusal listed %s", long)
	}
	if _, err := l.revoke("w1"); err != nil {
		t.Errorf("revoking a short name within the limit: %v", err)
	}
	for name := range data {
		if _, err := l.revoke(name); err != nil {
			t.Errorf("revoking %s again: %v", name, err)
		}
		break
	}
}

// asList reads a ConfigMap's data as the revoked nodes.
func asList(data map[string]string) revoked.List {
	l := revoked.List{}
	for name, value := range data {
		at, _ := time.Parse(revoked.TimeFormat, value)
		l[name] = at
	}
	return l
}
