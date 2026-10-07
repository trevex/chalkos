package pki

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestGenerateSecretsHasKubernetes(t *testing.T) {
	s := generate(t)
	if s.Version != 2 {
		t.Errorf("version = %d, want 2", s.Version)
	}
	k, err := s.RequireKubernetes()
	if err != nil {
		t.Fatal(err)
	}
	for name, ca := range map[string]CertKey{"ca": k.CA, "frontProxyCA": k.FrontProxyCA, "etcdCA": k.EtcdCA} {
		cert, _, err := ca.Parse()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !cert.IsCA || cert.NotAfter.Sub(cert.NotBefore) < CAValidity {
			t.Errorf("%s: IsCA %v, valid %v", name, cert.IsCA, cert.NotAfter.Sub(cert.NotBefore))
		}
	}
	if _, err := ParseECKey(k.ServiceAccountKey); err != nil {
		t.Errorf("service account key: %v", err)
	}
	if len(k.EncryptionKey) != EncryptionKeySize {
		t.Errorf("encryption key has %d bytes", len(k.EncryptionKey))
	}
	pub := s.Public()
	if pub.Kubernetes == nil || pub.Kubernetes.CA.Certificate != k.CA.Certificate || pub.Kubernetes.CA.Key != "" ||
		pub.Kubernetes.EtcdCA.Key != "" || pub.Kubernetes.FrontProxyCA.Key != "" {
		t.Errorf("public Kubernetes part = %+v, want the CA certificates only", pub.Kubernetes)
	}
}

// versionOne is a secrets file as chalkos wrote it before the Kubernetes secrets.
func versionOne(t *testing.T) Secrets {
	t.Helper()
	s := generate(t)
	s.Version = 1
	s.Kubernetes = nil
	return s
}

func TestReadSecretsVersionOne(t *testing.T) {
	v1 := versionOne(t)
	data, _ := v1.Encode()
	s, err := ReadSecrets(data, noIdentities)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequireKubernetes(); err == nil || !strings.Contains(err.Error(), "chalkctl secrets upgrade") {
		t.Errorf("RequireKubernetes = %v, want an error naming chalkctl secrets upgrade", err)
	}
}

func TestUpgradeKeepsSecrets(t *testing.T) {
	v1 := versionOne(t)
	s, err := Upgrade(v1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Version != 2 || s.OSCA != v1.OSCA || s.Admin != v1.Admin || !bytes.Equal(s.RecoverySecret, v1.RecoverySecret) {
		t.Error("upgrade changed the OS CA, the admin certificate or the recovery secret")
	}
	if _, err := Upgrade(s, now); err == nil {
		t.Error("upgraded a version 2 file")
	}
}

func TestValidateRejectsKubernetesMismatch(t *testing.T) {
	missing := generate(t)
	missing.Kubernetes = nil
	extra := versionOne(t)
	extra.Kubernetes = generate(t).Kubernetes
	leaf, err := SelfSigned("leaf", now)
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
		"version 2 without kubernetes": missing,
		"version 1 with kubernetes":    extra,
		"etcd CA not a CA":             notCA,
		"short encryption key":         shortKey,
		"service account key":          badSA,
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSecretsStringRedactsKubernetes(t *testing.T) {
	s := generate(t)
	out := fmt.Sprintf("%v %+v %#v", s, s, s)
	if strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, base64.StdEncoding.EncodeToString(s.Kubernetes.EncryptionKey)) {
		t.Errorf("formatted secrets leak Kubernetes keys: %s", out)
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

func TestKubernetesSecretsAndCertKeyRedacted(t *testing.T) {
	k := generate(t).Kubernetes
	ck := k.CA
	out := fmt.Sprintf("%v %+v %#v %v %+v %#v %v %+v %#v", k, k, k, ck, ck, ck, &ck, &ck, &ck)
	for name, secret := range map[string]string{
		"a private key":                 "PRIVATE KEY",
		"the encryption key in base64":  base64.StdEncoding.EncodeToString(k.EncryptionKey),
		"the encryption key in hex":     hex.EncodeToString(k.EncryptionKey),
		"the encryption key as numbers": fmt.Sprint(k.EncryptionKey),
	} {
		if strings.Contains(out, secret) {
			t.Errorf("formatted Kubernetes secrets contain %s", name)
		}
	}
	if !strings.Contains(out, "chalkos Kubernetes CA") {
		t.Error("formatted Kubernetes secrets do not name the CA")
	}
}
