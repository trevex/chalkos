package chalkctl

import (
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// TestRotateOneAtATime checks that a rotation refuses another, and the finish of another kind.
func TestRotateOneAtATime(t *testing.T) {
	l := newRotationLab(t)
	if err := l.rotate("service-account-key"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"os-ca"}, {"service-account-key"}, {"encryption-key", "--finish"}, {"os-ca", "--resume"}} {
		if err := l.rotate(args...); err == nil || !strings.Contains(err.Error(), "service-account key") && !strings.Contains(err.Error(), "service-account-key") {
			t.Errorf("rotate %v while the service-account key rotates: %v", args, err)
		}
	}
	if err := l.rotate("os-ca", "--force"); err == nil {
		t.Error("--force without --finish was accepted")
	}
}

// TestRotateKubernetesCA checks the Kubernetes CAs' rotation: the pause after the accept phase,
// the addons restarted, control planes before workers, and kubelets' certificates of the new CA.
func TestRotateKubernetesCA(t *testing.T) {
	l := newRotationLab(t)
	if err := l.rotate("kubernetes-ca"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "accept applied" {
		t.Fatalf("the secrets file records %q, want the accept phase applied and the rotation paused", l.phase())
	}
	events := l.takeEvents()
	if !slices.Contains(events, "cp1 RESTART_ADDONS") {
		t.Errorf("the addons were not restarted: %v", events)
	}
	if i, j := slices.Index(events, "cp2 share"), slices.Index(events, "w1 share"); i < 0 || j < i {
		t.Errorf("events %v: want the control planes' shares before the worker's", events)
	}
	for _, want := range []string{"--resume", "chalkctl kubeconfig", "carry both CAs", "stop verifying the API server at the switch", "chalkos.cluster.manifests, StatefulSets included", "not waited for: apps/DaemonSet/on-delete"} {
		if !strings.Contains(l.ta.stdout.String(), want) {
			t.Errorf("the pause after the accept phase does not say %q: %s", want, l.ta.stdout)
		}
	}
	l.ta.stdout.Reset()
	if err := l.rotate("kubernetes-ca", "--resume"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cannot verify the API server any more", "refused after the finish", "--finish"} {
		if !strings.Contains(l.ta.stdout.String(), want) {
			t.Errorf("the pause after the refresh does not say %q: %s", want, l.ta.stdout)
		}
	}
	s := l.secrets()
	ca := fingerprint(t, s.Kubernetes.CA.Certificate)
	for name, n := range l.nodes {
		if n.kind == "" {
			continue
		}
		if n.kubeletServing != ca || n.kubeletClient != ca {
			t.Errorf("%s's kubelet has certificates of another CA", name)
		}
		if n.kind == manifest.KindControlPlane && n.share.CA.Certificate != s.Kubernetes.CA.Certificate {
			t.Errorf("%s does not issue with the new CA", name)
		}
	}
	if err := l.rotate("kubernetes-ca", "--finish"); err != nil {
		t.Fatal(err)
	}
	for name, n := range l.nodes {
		if n.share != nil && n.share.Accepted != nil {
			t.Errorf("%s still accepts %v", name, n.share.Accepted)
		}
	}
}

// TestRotateControlPlanesInTurn checks, for every kind that restarts control planes, that a
// control plane takes a phase only once etcd is healthy and the control plane before runs on its
// new files.
func TestRotateControlPlanesInTurn(t *testing.T) {
	for _, kind := range []string{pki.RotateKubernetesCA, pki.RotateServiceAccountKey, pki.RotateEncryptionKey} {
		t.Run(kind, func(t *testing.T) {
			l := newRotationLab(t)
			if err := l.rotate(kind); err != nil {
				t.Fatal(err)
			}
			if kind == pki.RotateKubernetesCA {
				if err := l.rotate(kind, "--resume"); err != nil {
					t.Fatal(err)
				}
			}
			events := l.takeEvents()
			// Per phase: quorum, share and quorum again on cp1, then the same on cp2.
			var turns []string
			for _, e := range events {
				if strings.HasPrefix(e, "cp") && (strings.HasSuffix(e, " share") || strings.HasSuffix(e, " quorum")) {
					turns = append(turns, e)
				}
			}
			want := []string{"cp1 quorum", "cp1 share", "cp1 quorum", "cp2 quorum", "cp2 share", "cp2 quorum"}
			if len(turns) < 2*len(want) || !slices.Equal(turns[:len(want)], want) || !slices.Equal(turns[len(want):2*len(want)], want) {
				t.Errorf("events %v, want %v for the accept and the switch phases", turns, want)
			}
			// Only the Kubernetes CAs reach other nodes, after the control planes.
			if kind != pki.RotateKubernetesCA && slices.ContainsFunc(events, func(e string) bool { return strings.HasPrefix(e, "w1") || strings.HasPrefix(e, "n1") }) {
				t.Errorf("a node without a control plane took part: %v", events)
			}
		})
	}
}

// TestRotateEncryptionKey checks that the refresh rewrites every encrypted object and that the
// finish waits until etcd holds none under the old key.
func TestRotateEncryptionKey(t *testing.T) {
	l := newRotationLab(t)
	if err := l.rotate("encryption-key"); err != nil {
		t.Fatal(err)
	}
	s := l.secrets()
	if l.encrypted[pki.DefaultEncryptionKeyName] != 0 || l.encrypted[s.Kubernetes.KeyName()] != 3 {
		t.Fatalf("etcd holds %v, want the 3 objects under the new key", l.encrypted)
	}
	// An object written under the old key, as from a backup, stops the finish.
	l.encrypted[pki.DefaultEncryptionKeyName] = 1
	if err := l.rotate("encryption-key", "--finish"); err == nil || !strings.Contains(err.Error(), "1 object encrypted with the old key") {
		t.Fatalf("err = %v, want the finish refused", err)
	}
	// --force overrides the service-account key's wait alone, never data left under the old key.
	if err := l.rotate("encryption-key", "--finish", "--force"); err == nil || !strings.Contains(err.Error(), "1 object encrypted with the old key") {
		t.Fatalf("--force: err = %v, want the finish refused", err)
	}
	if l.phase() != "refresh applied" {
		t.Errorf("a refused finish left %q", l.phase())
	}
	delete(l.encrypted, pki.DefaultEncryptionKeyName)
	if err := l.rotate("encryption-key", "--finish"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cp1", "cp2"} {
		if keys := l.nodes[name].share.EncryptionKeys(); len(keys) != 1 || keys[0].Name != s.Kubernetes.KeyName() {
			t.Errorf("%s decrypts with %v after the finish", name, keys)
		}
	}
}

// TestRotateServiceAccountKeyWaitsAnHour checks that the service-account key's finish waits an
// hour after every control plane applied the switch, unless forced, and that the pause lists the
// Secrets of legacy tokens and says when the finish may follow.
func TestRotateServiceAccountKeyWaitsAnHour(t *testing.T) {
	l := newRotationLab(t)
	var mu sync.Mutex
	now := time.Now().Truncate(time.Second)
	start := now
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }
	l.ta.clock = clock
	// Each control plane takes ten minutes to restart on its new files.
	l.onShare = func(string) { advance(10 * time.Minute) }
	if err := l.rotate("service-account-key"); err != nil {
		t.Fatal(err)
	}
	l.onShare = nil
	// The accept and the switch phases each restarted both control planes.
	switched := l.secrets().Rotation.Switched
	if want := start.Add(40 * time.Minute); !switched.Equal(want) {
		t.Errorf("the switch is recorded at %v, want %v, once every control plane applied it", switched, want)
	}
	stdout := l.ta.stdout.String()
	if !strings.Contains(stdout, "chalkos.cluster.manifests, StatefulSets included") {
		t.Errorf("the pause does not say which workloads restarted: %s", stdout)
	}
	if !strings.Contains(stdout, "default/legacy") {
		t.Errorf("the pause does not list the Secrets of legacy tokens: %s", stdout)
	}
	if at := switched.Add(time.Hour).Local().Format(time.RFC3339); !strings.Contains(stdout, at) {
		t.Errorf("the pause does not name %s, an hour after the switch was applied: %s", at, stdout)
	}
	if events := l.takeEvents(); !slices.Contains(events, "cp1 RESTART_ADDONS") {
		t.Errorf("the addons did not restart with tokens of the new key: %v", events)
	}
	advance(switched.Add(59 * time.Minute).Sub(clock()))
	if err := l.rotate("service-account-key", "--finish"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal within the hour naming --force", err)
	}
	if err := l.rotate("service-account-key", "--finish", "--force"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "" {
		t.Errorf("the secrets file still records %q", l.phase())
	}
	s := l.secrets()
	pubs, _ := s.Kubernetes.ServiceAccountPublicKeys()
	for _, name := range []string{"cp1", "cp2"} {
		if got, _ := l.nodes[name].share.ServiceAccountPublicKeys(); !slices.Equal(got, pubs) || len(got) != 1 {
			t.Errorf("%s accepts %d service-account keys after the finish", name, len(got))
		}
	}
}

// TestRotateNamesAnUnreachableControlPlane checks that a control plane whose chalkd does not
// answer stops the phase promptly, naming it, rather than after the whole timeout.
func TestRotateNamesAnUnreachableControlPlane(t *testing.T) {
	l := newRotationLab(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.addrs["cp2"] = ln.Addr().String()
	ln.Close()
	began := time.Now()
	err = l.rotate("encryption-key", "--timeout", "30s")
	if err == nil || !strings.Contains(err.Error(), "cp2") || !strings.Contains(err.Error(), "does not answer") {
		t.Fatalf("err = %v, want the phase stopped naming cp2", err)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("naming the unreachable node took %v", took)
	}
}

// TestRotateRewriteTimeoutSuggestsMoreTime checks that a rewrite of the encrypted objects that
// outlasts --timeout says to resume with a larger one, and that resuming repeats it safely.
func TestRotateRewriteTimeoutSuggestsMoreTime(t *testing.T) {
	l := newRotationLab(t)
	l.mu.Lock()
	l.slowRewrite = true
	l.mu.Unlock()
	err := l.rotate("encryption-key", "--timeout", "1s")
	for _, want := range []string{"longer than --timeout 1s", "--resume --timeout 2s", "repeats the rewrite safely"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to say %q", err, want)
		}
	}
	if l.phase() != pki.PhaseRefresh {
		t.Fatalf("the secrets file records %q, want the refresh phase not applied", l.phase())
	}
	l.mu.Lock()
	l.slowRewrite = false
	l.mu.Unlock()
	if err := l.rotate("encryption-key", "--resume", "--timeout", "2s"); err != nil {
		t.Fatal(err)
	}
	if l.phase() != "refresh applied" {
		t.Errorf("the secrets file records %q after the resumed rewrite", l.phase())
	}
}

// TestRotateFinishSaysWhatToReissue checks that the finish of a CA's rotation says that client
// files or kubeconfigs issued during it still trust the old CA until they are issued again.
func TestRotateFinishSaysWhatToReissue(t *testing.T) {
	for kind, want := range map[string][]string{
		pki.RotateOSCA:         {"Client files issued during the rotation still trust the old OS CA", "chalkctl config new"},
		pki.RotateKubernetesCA: {"Kubeconfigs issued during the rotation still trust the old Kubernetes CA", "chalkctl kubeconfig"},
	} {
		t.Run(kind, func(t *testing.T) {
			l := newRotationLab(t)
			for _, args := range [][]string{{kind}, {kind, "--resume"}} {
				if err := l.rotate(args...); err != nil {
					t.Fatal(err)
				}
			}
			l.ta.stdout.Reset()
			if err := l.rotate(kind, "--finish"); err != nil {
				t.Fatal(err)
			}
			for _, w := range want {
				if !strings.Contains(l.ta.stdout.String(), w) {
					t.Errorf("the finish does not say %q: %s", w, l.ta.stdout)
				}
			}
		})
	}
}
