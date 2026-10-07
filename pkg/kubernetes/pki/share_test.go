package pki

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func secrets(t *testing.T) *pki.KubernetesSecrets {
	t.Helper()
	k, err := pki.NewKubernetesSecrets(now)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func roots(t *testing.T, ca pki.CertKey) *x509.CertPool {
	t.Helper()
	cert, err := pki.ParseCertificate([]byte(ca.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	p.AddCert(cert)
	return p
}

func TestShareByKind(t *testing.T) {
	k := secrets(t)
	cp, err := ShareFor(k, kubernetes.KindControlPlane, "cp1", now)
	if err != nil {
		t.Fatal(err)
	}
	if cp.CA != k.CA || *cp.EtcdCA != k.EtcdCA || *cp.FrontProxyCA != k.FrontProxyCA || cp.ServiceAccountKey != k.ServiceAccountKey ||
		string(cp.EncryptionKey) != string(k.EncryptionKey) || cp.Kubelet != nil {
		t.Error("the control-plane share lacks a secret of the control plane")
	}

	w, err := ShareFor(k, kubernetes.KindWorker, "w1", now)
	if err != nil {
		t.Fatal(err)
	}
	if w.CA.Key != "" || w.FrontProxyCA != nil || w.EtcdCA != nil || w.ServiceAccountKey != "" || w.EncryptionKey != nil {
		t.Errorf("the worker share holds a control-plane secret: %+v", w.Kind)
	}
	cert, _, err := w.Kubelet.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "system:node:w1" || strings.Join(cert.Subject.Organization, ",") != "system:nodes" {
		t.Errorf("kubelet certificate subject %v", cert.Subject)
	}
	if cert.NotAfter.Sub(now) != pki.LeafValidity {
		t.Errorf("kubelet certificate valid until %v", cert.NotAfter)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots(t, k.CA), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("kubelet certificate: %v", err)
	}
	if w.Node() != "w1" {
		t.Errorf("Node() = %q", w.Node())
	}
	if _, err := ShareFor(k, "etcd", "e1", now); err == nil {
		t.Error("issued a share of an unknown kind")
	}
}

func TestParseShare(t *testing.T) {
	k := secrets(t)
	for _, kind := range []string{kubernetes.KindControlPlane, kubernetes.KindWorker} {
		s, err := ShareFor(k, kind, "n1", now)
		if err != nil {
			t.Fatal(err)
		}
		data, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseShare(data); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

func TestShareValidateRefuses(t *testing.T) {
	k := secrets(t)
	other := secrets(t)
	cp := ControlPlaneShare(k)
	w, err := WorkerShare(k, "w1", now)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := WorkerShare(other, "w1", now)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := IssueAdmin(k.CA, "admin", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func() Share{
		"unknown kind":             func() Share { s := cp; s.Kind = "etcd"; return s },
		"control plane, no etcd":   func() Share { s := cp; s.EtcdCA = nil; return s },
		"control plane, short key": func() Share { s := cp; s.EncryptionKey = []byte("short"); return s },
		"worker with the CA key":   func() Share { s := w; s.CA = k.CA; return s },
		"worker, no kubelet":       func() Share { s := w; s.Kubelet = nil; return s },
		"worker, foreign kubelet":  func() Share { s := w; s.Kubelet = foreign.Kubelet; return s },
		"worker, admin as kubelet": func() Share { s := w; s.Kubelet = &admin; return s },
		"worker with an SA key":    func() Share { s := w; s.ServiceAccountKey = k.ServiceAccountKey; return s },
	} {
		if err := edit().Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestShareStringRedacted(t *testing.T) {
	s := ControlPlaneShare(secrets(t))
	out := fmt.Sprintf("%v %+v %#v", s, s, s)
	if strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("formatted share leaks a key: %s", out)
	}
}

func TestKubeconfig(t *testing.T) {
	k := secrets(t)
	admin, err := IssueAdmin(k.CA, "admin", 8760*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := admin.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cert.Subject.Organization, ",") != "chalkos:cluster-admins" || cert.NotAfter.Sub(now) != 8760*time.Hour {
		t.Errorf("admin certificate: %v until %v", cert.Subject, cert.NotAfter)
	}
	data, err := Kubeconfig{Name: "lab", Server: "https://127.0.0.1:16443", ServerName: "10.0.0.10", CA: []byte(k.CA.Certificate), Client: admin}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var kc struct {
		Clusters []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server        string `json:"server"`
				TLSServerName string `json:"tls-server-name"`
				CAData        []byte `json:"certificate-authority-data"`
			} `json:"cluster"`
		} `json:"clusters"`
		Users []struct {
			User struct {
				CertData []byte `json:"client-certificate-data"`
				KeyData  []byte `json:"client-key-data"`
			} `json:"user"`
		} `json:"users"`
		CurrentContext string `json:"current-context"`
	}
	if err := json.Unmarshal(data, &kc); err != nil {
		t.Fatal(err)
	}
	c := kc.Clusters[0].Cluster
	if c.Server != "https://127.0.0.1:16443" || c.TLSServerName != "10.0.0.10" || string(c.CAData) != k.CA.Certificate ||
		string(kc.Users[0].User.CertData) != admin.Certificate || string(kc.Users[0].User.KeyData) != admin.Key || kc.CurrentContext != "lab" {
		// The kubeconfig embeds the client key, so describe it rather than print it.
		t.Errorf("kubeconfig: server %q, tls-server-name %q, context %q, or its CA, certificate or key differ",
			c.Server, c.TLSServerName, kc.CurrentContext)
	}

	files, err := Kubeconfig{Name: "lab", Server: "https://10.0.0.10:6443", CAFile: "/ca.crt", ClientFile: "/client.pem"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files), `"client-certificate": "/client.pem"`) || !strings.Contains(string(files), `"certificate-authority": "/ca.crt"`) {
		t.Errorf("kubeconfig with files = %s", files)
	}
	if _, err := (Kubeconfig{Name: "lab", Server: "https://x"}).Encode(); err == nil {
		t.Error("encoded a kubeconfig without credentials")
	}
	if _, err := IssueAdmin(k.CA, "admin", 0, now); err == nil {
		t.Error("issued an admin certificate without validity")
	}
}

func TestControlPlaneShareRejectsSharedCAs(t *testing.T) {
	k := secrets(t)
	for _, tc := range []struct {
		name, a, b string
		edit       func(s *Share)
	}{
		{"etcd CA is the Kubernetes CA", "ca", "etcdCA", func(s *Share) { ca := s.CA; s.EtcdCA = &ca }},
		{"front-proxy CA is the Kubernetes CA", "ca", "frontProxyCA", func(s *Share) { ca := s.CA; s.FrontProxyCA = &ca }},
		{"etcd CA is the front-proxy CA", "frontProxyCA", "etcdCA", func(s *Share) { ca := *s.FrontProxyCA; s.EtcdCA = &ca }},
	} {
		s := ControlPlaneShare(k)
		tc.edit(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, tc.a+" and "+tc.b) {
			t.Errorf("%s: error %q does not name %s and %s", tc.name, msg, tc.a, tc.b)
		}
		if strings.Contains(msg, "PRIVATE KEY") || strings.Contains(msg, "CERTIFICATE") {
			t.Errorf("%s: error contains key material: %q", tc.name, msg)
		}
	}
}

func TestParseShareStrict(t *testing.T) {
	k := secrets(t)
	w, err := WorkerShare(k, "w1", now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := w.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["caKey"] = k.CA.Key
	extra, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseShare(extra); err == nil {
		t.Error("accepted a worker share with an unknown caKey field")
	} else if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Errorf("error contains a key: %v", err)
	}
	for name, trailing := range map[string][]byte{
		"garbage":       append(append([]byte{}, data...), []byte("garbage")...),
		"second object": append(append([]byte{}, data...), data...),
	} {
		if _, err := ParseShare(trailing); err == nil {
			t.Errorf("accepted a share followed by %s", name)
		}
	}
	if _, err := ParseShare(append(append([]byte{}, data...), '\n')); err != nil {
		t.Errorf("rejected a share with a trailing newline: %v", err)
	}
}

func TestShareValidateKubeletSubject(t *testing.T) {
	k := secrets(t)
	w, err := WorkerShare(k, "w1", now)
	if err != nil {
		t.Fatal(err)
	}
	for name, leaf := range map[string]pki.Leaf{
		"in system:masters":           {CommonName: NodeUserPrefix + "w1", Organization: []string{MastersGroup}, Client: true},
		"in system:nodes and masters": {CommonName: NodeUserPrefix + "w1", Organization: []string{NodesGroup, MastersGroup}, Client: true},
		"without a name":              {CommonName: NodeUserPrefix, Organization: []string{NodesGroup}, Client: true},
		"with an invalid name":        {CommonName: NodeUserPrefix + "W_1", Organization: []string{NodesGroup}, Client: true},
	} {
		ck, err := pki.IssueLeaf(k.CA, leaf, now)
		if err != nil {
			t.Fatal(err)
		}
		s := w
		s.Kubelet = &ck
		if err := s.Validate(); err == nil {
			t.Errorf("accepted a kubelet certificate %s", name)
		}
	}
	if err := w.Validate(); err != nil {
		t.Errorf("rejected a valid worker share: %v", err)
	}
}

func TestShareValidateFor(t *testing.T) {
	k := secrets(t)
	w2, err := WorkerShare(k, "w2", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.ValidateFor("w1"); err == nil {
		t.Error("w1 accepted a share for w2")
	}
	if err := w2.ValidateFor("w2"); err != nil {
		t.Errorf("w2 rejected its share: %v", err)
	}
	if err := ControlPlaneShare(k).ValidateFor("cp1"); err != nil {
		t.Errorf("cp1 rejected its share: %v", err)
	}
	bad := w2
	bad.ServiceAccountKey = k.ServiceAccountKey
	if err := bad.ValidateFor("w2"); err == nil {
		t.Error("ValidateFor accepted a share Validate rejects")
	}
}
