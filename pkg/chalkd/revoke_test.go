package chalkd

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
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
	if _, err := l.revoke(""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("revoking no name: %v, want InvalidArgument", err)
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
