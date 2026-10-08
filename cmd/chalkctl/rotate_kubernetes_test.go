package main

import (
	"slices"
	"strings"
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
	if !strings.Contains(l.ta.stdout.String(), "--resume") {
		t.Errorf("the pause does not say how to continue: %s", l.ta.stdout)
	}
	if err := l.rotate("kubernetes-ca", "--resume"); err != nil {
		t.Fatal(err)
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

// TestRotateControlPlanesInTurn checks that a control plane takes a phase only once etcd is
// healthy and the control plane before runs on its new files.
func TestRotateControlPlanesInTurn(t *testing.T) {
	l := newRotationLab(t)
	if err := l.rotate("encryption-key"); err != nil {
		t.Fatal(err)
	}
	events := l.takeEvents()
	// Per phase: quorum, share and quorum again on cp1, then the same on cp2.
	var accept []string
	for _, e := range events {
		if strings.HasSuffix(e, " share") || strings.HasSuffix(e, " quorum") {
			accept = append(accept, e)
		}
	}
	want := []string{"cp1 quorum", "cp1 share", "cp1 quorum", "cp2 quorum", "cp2 share", "cp2 quorum"}
	if len(accept) < 2*len(want) || !slices.Equal(accept[:len(want)], want) || !slices.Equal(accept[len(want):2*len(want)], want) {
		t.Errorf("events %v, want %v for the accept and the switch phases", accept, want)
	}
	if slices.ContainsFunc(events, func(e string) bool { return strings.HasPrefix(e, "w1") || strings.HasPrefix(e, "n1") }) {
		t.Errorf("a node without a control plane took part: %v", events)
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
	if err := l.rotate("encryption-key", "--finish"); err == nil || !strings.Contains(err.Error(), "1 objects encrypted with the old key") {
		t.Fatalf("err = %v, want the finish refused", err)
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
// hour after the switch unless forced, and that the pause lists the Secrets of legacy tokens.
func TestRotateServiceAccountKeyWaitsAnHour(t *testing.T) {
	l := newRotationLab(t)
	now := time.Now()
	l.ta.clock = func() time.Time { return now }
	if err := l.rotate("service-account-key"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.ta.stdout.String(), "default/legacy") {
		t.Errorf("the pause does not list the Secrets of legacy tokens: %s", l.ta.stdout)
	}
	if events := l.takeEvents(); !slices.Contains(events, "cp1 RESTART_ADDONS") {
		t.Errorf("the addons did not restart with tokens of the new key: %v", events)
	}
	now = now.Add(30 * time.Minute)
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
