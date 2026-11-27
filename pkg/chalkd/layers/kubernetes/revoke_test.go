package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/testwait"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
	"github.com/trevex/chalkos/pkg/pki"
)

// revokeLab is a bootstrapped control plane whose API server is a fake holding w1's Node.
type revokeLab struct {
	s  *Plugin
	cs *fake.Clientset
}

func newRevokeLab(t *testing.T) *revokeLab {
	t.Helper()
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	write(t, s.k.state.BootstrappedMarker(), "")
	l := &revokeLab{s: s, cs: fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}})}
	s.k.hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return l.cs, nil }
	return l
}

// trustOSCAs makes the node trust, besides the OS CA that issued its certificate, an OS CA made at.
func (l *revokeLab) trustOSCAs(t *testing.T, at time.Time) {
	t.Helper()
	own, err := kubernetestest.OSCA()
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.NewOSCA(at)
	if err != nil {
		t.Fatal(err)
	}
	l.s.node.(*fakeNode).cert = &fakeCertificate{osCA: pki.Bundle(own.Certificate, other.Certificate)}
}

// kubernetesCAs gives the node a share whose Kubernetes CA was made at, and which accepts a
// Kubernetes CA made at each of accepted.
func (l *revokeLab) kubernetesCAs(t *testing.T, at time.Time, accepted ...time.Time) {
	t.Helper()
	k, err := clustersecrets.NewKubernetesSecrets(at)
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
	data, err := kpki.ControlPlaneShare(k, kubernetestest.NodeCA(t)).Encode()
	if err != nil {
		t.Fatal(err)
	}
	write(t, l.s.k.state.Share(), string(data))
}

func (l *revokeLab) list(t *testing.T) revoked.List {
	t.Helper()
	list, err := revoked.Load(context.Background(), l.cs)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func (l *revokeLab) revoke(name string) (*kubernetesv1.RevokeNodeResponse, error) {
	resp, err := l.s.RevokeNode(context.Background(), &kubernetesv1.RevokeNodeRequest{Node: name})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (l *revokeLab) unrevoke(name string, force bool) (*kubernetesv1.UnrevokeNodeResponse, error) {
	resp, err := l.s.UnrevokeNode(context.Background(), &kubernetesv1.UnrevokeNodeRequest{Node: name, Force: force})
	if err != nil {
		return nil, err
	}
	return resp, nil
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
	if want := uint32(revoked.LiteralLength(revoked.List{"w1": at})); got.ListLength != want || got.ListCapacity != revoked.MaxLiteralLength {
		t.Errorf("list %d of %d, want %d of %d", got.ListLength, got.ListCapacity, want, revoked.MaxLiteralLength)
	}
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
		if _, err := l.revoke(name); layer.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("revoking %q: %v, want InvalidArgument", name, err)
		}
		if _, err := l.unrevoke(name, true); layer.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("unrevoking %q: %v, want InvalidArgument", name, err)
		}
	}
}

// TestRevokeNodeRefresh lists a revoked node as revoked now, applies the policy with the new time
// and keeps an expired entry it refreshes; a node not revoked is refused and stays unlisted.
func TestRevokeNodeRefresh(t *testing.T) {
	l := newRevokeLab(t)
	l.revokeAt(t, map[string]time.Time{"w1": expiredAt(), "w-ancient": expiredAt()})
	before := time.Now().Add(-time.Second)
	got, err := l.s.RevokeNode(context.Background(), &kubernetesv1.RevokeNodeRequest{Node: "w1", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	at := got.RevokedAt.AsTime()
	if at.Before(before) || at.After(time.Now()) || !got.NodeDeleted {
		t.Errorf("refreshed at %v, Node deleted %v; want now and deleted", at, got.NodeDeleted)
	}
	list := l.list(t)
	if !list["w1"].Equal(at) {
		t.Errorf("revoked nodes %v, want w1 at %v", list, at)
	}
	if _, ok := list["w-ancient"]; !ok {
		t.Error("a refresh dropped an expired entry")
	}
	if want := uint32(revoked.LiteralLength(list)); got.ListLength != want || got.ListCapacity != revoked.MaxLiteralLength {
		t.Errorf("list %d of %d, want %d of %d", got.ListLength, got.ListCapacity, want, revoked.MaxLiteralLength)
	}
	if policy := l.policyNames(t); !strings.Contains(policy, at.UTC().Format(revoked.TimeFormat)) {
		t.Errorf("the policy %s does not name w1's new time", policy)
	}
	_, err = l.s.RevokeNode(context.Background(), &kubernetesv1.RevokeNodeRequest{Node: "w2", Refresh: true})
	if layer.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("refreshing w2, not revoked: %v, want NotFound", err)
	}
	if _, ok := l.list(t)["w2"]; ok {
		t.Error("refreshing w2, not revoked, lists it")
	}

	// A refresh that fails after the list was written says so, and that refreshing again repairs it.
	policyFails := true
	l.cs.PrependReactor("patch", "validatingadmissionpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		return policyFails, nil, errors.New("connection reset")
	})
	_, err = l.s.RevokeNode(context.Background(), &kubernetesv1.RevokeNodeRequest{Node: "w1", Refresh: true})
	if err == nil || !strings.Contains(err.Error(), "the revocation of w1 is refreshed, but the API server's admission policy was not updated") ||
		!strings.Contains(err.Error(), "refreshing it again applies it") {
		t.Errorf("a refresh with the policy failing: %v", err)
	}
	l.cs.PrependReactor("delete", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset")
	})
	policyFails = false
	_, err = l.s.RevokeNode(context.Background(), &kubernetesv1.RevokeNodeRequest{Node: "w1", Refresh: true})
	if err == nil || !strings.Contains(err.Error(), "the revocation of w1 is refreshed, but deleting its Node failed") ||
		!strings.Contains(err.Error(), "refreshing it again deletes it") {
		t.Errorf("a refresh with the Node's deletion failing: %v", err)
	}
}

// TestRevokeNodeOnControlPlanesOnly refuses the calls on the revoked nodes where no list is kept.
func TestRevokeNodeOnControlPlanesOnly(t *testing.T) {
	worker, _ := kubernetesNode(t, k8s.KindWorker, true)
	unbootstrapped, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	for name, tc := range map[string]struct {
		s    *Plugin
		want string
	}{
		"a worker":                  {worker, "ask a control-plane node"},
		"an unbootstrapped control": {unbootstrapped, "not an etcd member"},
	} {
		_, err := tc.s.RevokeNode(context.Background(), &kubernetesv1.RevokeNodeRequest{Node: "w1"})
		if layer.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("revoke on %s: %v, want a refusal naming %q", name, err, tc.want)
		}
		_, err = tc.s.UnrevokeNode(context.Background(), &kubernetesv1.UnrevokeNodeRequest{Node: "w1"})
		if layer.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("unrevoke on %s: %v, want FailedPrecondition", name, err)
		}
		_, err = tc.s.PruneRevoked(context.Background(), &kubernetesv1.PruneRevokedRequest{})
		if layer.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("prune on %s: %v, want FailedPrecondition", name, err)
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
				if layer.CodeOf(err) != connect.CodeFailedPrecondition {
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
	if _, err := l.unrevoke("w1", true); layer.CodeOf(err) != connect.CodeNotFound {
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
	if layer.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "chalkctl rotate os-ca") || !strings.Contains(err.Error(), "chalkctl rotate kubernetes-ca") {
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
	st, err := l.s.k.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := st.RevokedNodes
	if len(got) != 2 || got[0].Name != "w1" || got[0].RevokedAt != nil || got[1].Name != "w2" || got[1].RevokedAt.AsTime().Format(revoked.TimeFormat) != cm.Data["w2"] || st.RevokedNodesError != "" {
		t.Errorf("revoked nodes %v, error %q; want w1 at an unknown time and w2 at %s", got, st.RevokedNodesError, cm.Data["w2"])
	}
	if want := uint32(revoked.LiteralLength(l.list(t))); st.RevokedListLength != want || st.RevokedListCapacity != revoked.MaxLiteralLength {
		t.Errorf("status list %d of %d, want %d of %d", st.RevokedListLength, st.RevokedListCapacity, want, revoked.MaxLiteralLength)
	}

	l.s.k.hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
		return nil, errors.New("connection refused")
	}
	if st, err = l.s.k.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.RevokedNodes) != 0 || !strings.Contains(st.RevokedNodesError, "connection refused") || !strings.HasPrefix(st.State, "bootstrapped") {
		t.Errorf("without the API server: revoked nodes %v, error %q, state %q", st.RevokedNodes, st.RevokedNodesError, st.State)
	}
	if st.RevokedListLength != 0 || st.RevokedListCapacity != revoked.MaxLiteralLength {
		t.Errorf("without the API server: list %d of %d, want 0 of %d", st.RevokedListLength, st.RevokedListCapacity, revoked.MaxLiteralLength)
	}

	worker, _ := kubernetesNode(t, k8s.KindWorker, true)
	if st, err := worker.k.status(context.Background()); err != nil || st.RevokedNodes != nil || st.RevokedNodesError != "" || st.RevokedListCapacity != 0 {
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
	if _, err := l.unrevoke("w2", true); layer.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unrevoking again: %v, want NotFound", err)
	}
	if got := l.policyNames(t); got != "{}" {
		t.Errorf("unrevoking again: the policy names %s", got)
	}
}

// TestStatusReadsTheNodeWhileTheListHangs bounds the read of the revoked nodes and that of the
// Node apart, so a read that hangs leaves the other its time.
func TestStatusReadsTheNodeWhileTheListHangs(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		l := newRevokeLab(t)
		k := l.s.k
		l.cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			time.Sleep(statusTimeout)
			return true, nil, context.DeadlineExceeded
		})
		k.hooks.Node = func(ctx context.Context) (*corev1.Node, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return kubernetestest.ReadyNode(), nil
		}
		st, err := k.status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.NodeReady != "True" || st.RevokedNodesError == "" {
			t.Errorf("node ready %q, revoked nodes error %q; want the Node read and the list's error", st.NodeReady, st.RevokedNodesError)
		}
	})
}

// A revocation that would make the admission policy's map of revoked nodes longer than the API
// server compiles is refused before the node is listed; one already listed is revoked again.
func TestRevokeNodeRefusesPastThePolicysLimit(t *testing.T) {
	l := newRevokeLab(t)
	// A Kubernetes CA older than the revocations keeps them from being pruned.
	l.kubernetesCAs(t, time.Now().Add(-72*time.Hour))
	data := map[string]string{}
	at := time.Now().Add(-48 * time.Hour).Format(revoked.TimeFormat)
	for i := 0; revoked.LiteralLength(asList(data)) <= revoked.MaxLiteralLength-200; i++ {
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
	if layer.CodeOf(err) != connect.CodeFailedPrecondition || client.Reason(err) != nodev1.ErrorReason_ERROR_REASON_REVOKED_LIST_FULL ||
		!strings.Contains(err.Error(), "chalkctl rotate os-ca") || !strings.Contains(err.Error(), "chalkctl rotate kubernetes-ca") {
		t.Errorf("revoking %s: %v, want REVOKED_LIST_FULL naming the rotations", long, err)
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

// revokeAt lists each node as revoked at its time.
func (l *revokeLab) revokeAt(t *testing.T, nodes map[string]time.Time) {
	t.Helper()
	for name, at := range nodes {
		if _, err := revoked.Revoke(context.Background(), l.cs, name, at); err != nil {
			t.Fatal(err)
		}
	}
}

// expiredAt is a revocation time whose node's certificates have all expired by now, a year past
// the margin the revoked nodes keep entries for after their validity.
func expiredAt() time.Time {
	return time.Now().Add(-2 * pki.LeafValidity)
}

// A revocation and an unrevocation first drop the entries whose certificates expired, and apply
// the policy without them. They keep an entry revoked before every CA this control plane trusts:
// another control plane may not have finished the CAs' rotation yet.
func TestRevokeNodePrunesExpired(t *testing.T) {
	for name, call := range map[string]func(*revokeLab) error{
		"revoke": func(l *revokeLab) error { _, err := l.revoke("w1"); return err },
		"unrevoke": func(l *revokeLab) error {
			if _, err := revoked.Revoke(context.Background(), l.cs, "w1", time.Now()); err != nil {
				return err
			}
			_, err := l.unrevoke("w1", true)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newRevokeLab(t)
			// newRevokeLab's CAs are made as the tests start, backdated an hour: w-rotated was
			// revoked before them.
			l.revokeAt(t, map[string]time.Time{"w-rotated": time.Now().Add(-48 * time.Hour), "w-ancient": expiredAt()})
			if err := call(l); err != nil {
				t.Fatal(err)
			}
			list := l.list(t)
			if _, ok := list["w-ancient"]; ok {
				t.Error("an entry past its certificates' validity is kept")
			}
			if _, ok := list["w-rotated"]; !ok {
				t.Error("an entry revoked before every trusted CA is dropped")
			}
			if names := l.policyNames(t); strings.Contains(names, "w-ancient") || !strings.Contains(names, "w-rotated") {
				t.Errorf("the policy names %s, want w-rotated and not w-ancient", names)
			}
		})
	}
}

// A revocation's list length counts the list after the prune.
func TestRevokeNodeListLength(t *testing.T) {
	l := newRevokeLab(t)
	l.revokeAt(t, map[string]time.Time{"w-old": time.Now().Add(-2 * time.Hour), "w-ancient": expiredAt()})
	got, err := l.revoke("w1")
	if err != nil {
		t.Fatal(err)
	}
	if want := uint32(revoked.LiteralLength(l.list(t))); got.ListLength != want {
		t.Errorf("list length %d, want %d", got.ListLength, want)
	}
}

// A prune that fails holds up no revocation or unrevocation.
func TestRevokeNodeDespiteAFailedPrune(t *testing.T) {
	l := newRevokeLab(t)
	l.revokeAt(t, map[string]time.Time{"w-ancient": expiredAt(), "w2": time.Now()})
	// The revocation prunes before it writes the list, the unrevocation after.
	var writes, failing int
	l.cs.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if writes++; writes == failing {
			return true, nil, errors.New("connection reset")
		}
		return false, nil, nil
	})
	failing = 1
	if _, err := l.revoke("w1"); err != nil {
		t.Fatalf("revoke with the prune failing: %v", err)
	}
	if _, ok := l.list(t)["w1"]; !ok {
		t.Error("w1 is not revoked")
	}
	writes, failing = 0, 2
	if _, err := l.unrevoke("w2", true); err != nil {
		t.Fatalf("unrevoke with the prune failing: %v", err)
	}
	if _, ok := l.list(t)["w2"]; ok {
		t.Error("w2 is still revoked")
	}
	if _, ok := l.list(t)["w-ancient"]; !ok {
		t.Error("the prunes did not fail")
	}
}

// PruneRevoked drops the entries whose certificates expired and those revoked before every OS CA
// and Kubernetes CA the control plane trusts, and applies the policy without them. It keeps those
// revoked after a CA was made, or at an unknown time.
func TestPruneRevoked(t *testing.T) {
	l := newRevokeLab(t)
	now := time.Now()
	// The OS CA is made as the tests start, backdated an hour; the Kubernetes CA was made a day
	// ago, so only w-before was revoked before both.
	l.kubernetesCAs(t, now.Add(-24*time.Hour))
	l.revokeAt(t, map[string]time.Time{
		"w-ancient": expiredAt(),
		"w-before":  now.Add(-48 * time.Hour),
		"w-between": now.Add(-2 * time.Hour),
		"w-recent":  now.Add(-30 * time.Minute),
	})
	cm, err := l.cs.CoreV1().ConfigMaps(revoked.Namespace).Get(context.Background(), revoked.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["w-unknown"] = "yesterday"
	if _, err := l.cs.CoreV1().ConfigMaps(revoked.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := l.s.PruneRevoked(context.Background(), &kubernetesv1.PruneRevokedRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"w-ancient", "w-before"}; !slices.Equal(got.Pruned, want) {
		t.Errorf("pruned %q, want %q", got.Pruned, want)
	}
	list := l.list(t)
	if want := []string{"w-between", "w-recent", "w-unknown"}; !slices.Equal(slices.Sorted(maps.Keys(list)), want) {
		t.Errorf("revoked nodes %v, want %q", list, want)
	}
	if want := uint32(revoked.LiteralLength(list)); got.ListLength != want || got.ListCapacity != revoked.MaxLiteralLength {
		t.Errorf("list %d of %d, want %d of %d", got.ListLength, got.ListCapacity, want, revoked.MaxLiteralLength)
	}
	if names := l.policyNames(t); strings.Contains(names, "w-before") || !strings.Contains(names, "w-between") {
		t.Errorf("the policy names %s, want w-between and not w-before", names)
	}

	again, err := l.s.PruneRevoked(context.Background(), &kubernetesv1.PruneRevokedRequest{})
	if err != nil || len(again.Pruned) != 0 || again.ListLength != got.ListLength {
		t.Errorf("pruning again: %v, %v; want nothing pruned", again, err)
	}
}

// PruneRevoked applies the policy even when it drops no node, so calling it again applies a
// policy an earlier call failed to.
func TestPruneRevokedAppliesThePolicy(t *testing.T) {
	l := newRevokeLab(t)
	l.revokeAt(t, map[string]time.Time{"w-before": time.Now().Add(-48 * time.Hour)})
	failing := true
	l.cs.PrependReactor("patch", "validatingadmissionpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failing {
			return true, nil, errors.New("connection reset")
		}
		return false, nil, nil
	})
	_, err := l.s.PruneRevoked(context.Background(), &kubernetesv1.PruneRevokedRequest{})
	if layer.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "w-before") || !strings.Contains(err.Error(), "again") {
		t.Errorf("prune with the policy failing: %v, want Unavailable naming w-before", err)
	}
	failing = false
	if _, err := l.s.PruneRevoked(context.Background(), &kubernetesv1.PruneRevokedRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := l.policyNames(t); got != "{}" {
		t.Errorf("pruning again: the policy names %s", got)
	}
}

// Without the CAs the node trusts, PruneRevoked drops nothing.
func TestPruneRevokedWithoutCAs(t *testing.T) {
	l := newRevokeLab(t)
	l.revokeAt(t, map[string]time.Time{"w-before": time.Now().Add(-48 * time.Hour)})
	l.s.node.(*fakeNode).cert = nil
	if _, err := l.s.PruneRevoked(context.Background(), &kubernetesv1.PruneRevokedRequest{}); layer.CodeOf(err) != connect.CodeInternal {
		t.Errorf("prune without an OS CA: %v, want Internal", err)
	}
	if _, ok := l.list(t)["w-before"]; !ok {
		t.Error("w-before was dropped")
	}
}

// The pruning loop drops only the entries whose certificates expired: one revoked before every
// CA this control plane trusts stays, as another control plane may not have finished the CAs'
// rotation yet.
func TestPruneExpired(t *testing.T) {
	l := newRevokeLab(t)
	// newRevokeLab's CAs are made as the tests start, backdated an hour: w-rotated was revoked
	// before them.
	l.revokeAt(t, map[string]time.Time{"w-ancient": expiredAt(), "w-rotated": time.Now().Add(-48 * time.Hour)})
	// The loop prunes with pruneExpired unless the options name another hook.
	lab := l.s.k
	k := newSide(Options{Paths: lab.state.Paths, ImageFile: lab.o.ImageFile, ClusterFile: lab.o.ClusterFile, Hooks: Hooks{APIClient: lab.hooks.APIClient}})
	if err := k.hooks.PruneRevoked(context.Background()); err != nil {
		t.Fatal(err)
	}
	list := l.list(t)
	if _, ok := list["w-ancient"]; ok {
		t.Error("an entry past its certificates' validity is kept")
	}
	if _, ok := list["w-rotated"]; !ok {
		t.Error("an entry revoked before every trusted CA is dropped by expiry alone")
	}
	if names := l.policyNames(t); strings.Contains(names, "w-ancient") || !strings.Contains(names, "w-rotated") {
		t.Errorf("the policy names %s, want w-rotated and not w-ancient", names)
	}
}

// A cluster whose controller-manager signs kubelet certificates for longer than a node
// certificate keeps entries for that long.
func TestCertValidity(t *testing.T) {
	for _, tc := range []struct {
		flags map[string]string
		want  time.Duration
	}{
		{nil, pki.LeafValidity},
		{map[string]string{"cluster-signing-duration": "17520h"}, 17520 * time.Hour},
		{map[string]string{"cluster_signing_duration": "17520h"}, 17520 * time.Hour},
		{map[string]string{"cluster-signing-duration": "24h"}, pki.LeafValidity},
		{map[string]string{"cluster-signing-duration": "forever"}, pki.LeafValidity},
	} {
		c := k8s.Cluster{ExtraArgs: map[string]map[string]string{"kube-controller-manager": tc.flags}}
		if got := certValidity(c); got != tc.want {
			t.Errorf("certValidity(%v) = %v, want %v", tc.flags, got, tc.want)
		}
	}
}

// The pruning loop's errors say once that pruning failed, whether reaching the API server,
// reading the list or applying the admission policy did.
func TestPruneExpiredErrors(t *testing.T) {
	l := newRevokeLab(t)
	l.revokeAt(t, map[string]time.Time{"w-ancient": expiredAt()})
	lab := l.s.k
	k := newSide(Options{Paths: lab.state.Paths, ImageFile: lab.o.ImageFile, ClusterFile: lab.o.ClusterFile, Hooks: Hooks{
		APIClient: func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
			return nil, errors.New("no route to host")
		},
	}})
	check := func(what, want string) {
		t.Helper()
		err := k.hooks.PruneRevoked(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "prune the revoked nodes: ") || strings.Count(err.Error(), "prune the revoked nodes") != 1 || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want the prefix once and %q", what, err, want)
		}
	}
	check("API server not reached", "no route to host")
	k.hooks.APIClient = lab.hooks.APIClient
	l.cs.PrependReactor("patch", "validatingadmissionpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset")
	})
	check("policy not applied", "connection reset")
	l.cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	check("list not read", "connection refused")
}
