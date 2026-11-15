package clustersecrets

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/pki"
)

func TestGenerateHasKubernetes(t *testing.T) {
	s := generate(t)
	if s.Version != 3 {
		t.Errorf("version = %d, want 3", s.Version)
	}
	k := s.Kubernetes
	for name, ca := range map[string]pki.CertKey{"ca": k.CA, "frontProxyCA": k.FrontProxyCA, "etcdCA": k.EtcdCA} {
		cert, _, err := ca.Parse()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !cert.IsCA || cert.NotAfter.Sub(cert.NotBefore) < pki.CAValidity {
			t.Errorf("%s: IsCA %v, valid %v", name, cert.IsCA, cert.NotAfter.Sub(cert.NotBefore))
		}
	}
	if _, err := pki.ParseECKey(k.ServiceAccountKey); err != nil {
		t.Errorf("service account key: %v", err)
	}
	if len(k.EncryptionKey) != EncryptionKeySize {
		t.Errorf("encryption key has %d bytes", len(k.EncryptionKey))
	}
	pub := s.Public()
	if pub.Kubernetes.CA.Certificate != k.CA.Certificate || pub.Kubernetes.CA.Key != "" ||
		pub.Kubernetes.EtcdCA.Key != "" || pub.Kubernetes.FrontProxyCA.Key != "" {
		t.Errorf("public Kubernetes part = %+v, want the CA certificates only", pub.Kubernetes)
	}
}

func TestValidateRejectsKubernetesMismatch(t *testing.T) {
	leaf, err := pki.SelfSigned("leaf", now)
	if err != nil {
		t.Fatal(err)
	}
	notCA := generate(t)
	notCA.Kubernetes.EtcdCA = leaf
	shortKey := generate(t)
	shortKey.Kubernetes.EncryptionKey = shortKey.Kubernetes.EncryptionKey[:16]
	badSA := generate(t)
	badSA.Kubernetes.ServiceAccountKey = "not a key"
	for name, s := range map[string]Secrets{
		"etcd CA not a CA":     notCA,
		"short encryption key": shortKey,
		"service account key":  badSA,
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateRejectsSharedCAs(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		edit       func(s *Secrets)
	}{
		{"etcd CA is the Kubernetes CA", "kubernetes.ca", "kubernetes.etcdCA", func(s *Secrets) { s.Kubernetes.EtcdCA = s.Kubernetes.CA }},
		{"front-proxy CA is the Kubernetes CA", "kubernetes.ca", "kubernetes.frontProxyCA", func(s *Secrets) { s.Kubernetes.FrontProxyCA = s.Kubernetes.CA }},
		{"etcd CA is the OS CA", "osCA", "kubernetes.etcdCA", func(s *Secrets) { s.Kubernetes.EtcdCA = s.OSCA }},
		{"Kubernetes CA is the node CA", "nodeCA", "kubernetes.ca", func(s *Secrets) { s.Kubernetes.CA = s.NodeCA }},
	} {
		s := generate(t)
		tc.edit(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, tc.a) || !strings.Contains(msg, tc.b) {
			t.Errorf("%s: error %q does not name %s and %s", tc.name, msg, tc.a, tc.b)
		}
		if strings.Contains(msg, "PRIVATE KEY") || strings.Contains(msg, "CERTIFICATE") {
			t.Errorf("%s: error contains key material: %q", tc.name, msg)
		}
	}

	k := generate(t).Kubernetes
	k.EtcdCA = k.CA
	if err := k.Validate(); err == nil {
		t.Error("KubernetesSecrets.Validate accepted an etcd CA that is the Kubernetes CA")
	}
}
