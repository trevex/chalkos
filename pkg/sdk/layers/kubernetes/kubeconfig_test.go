package kubernetes

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/trevex/chalkos/pkg/clustersecrets"
)

// An admin kubeconfig points at the cluster's endpoint, or at another URL whose certificate is
// verified for the endpoint's host, trusts the cluster's CA bundle and authenticates as the user.
func TestKubeconfigCarriesTheCABundle(t *testing.T) {
	now := time.Now()
	secrets, err := clustersecrets.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		via, server, serverName string
	}{
		{"", "https://10.0.0.10:6443", ""},
		{"https://127.0.0.1:16443", "https://127.0.0.1:16443", "10.0.0.10"},
	} {
		data, err := Kubeconfig(secrets, KubeconfigRequest{Context: "lab", Server: "https://10.0.0.10:6443", Via: tc.via, User: "alice", TTL: time.Hour}, now)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := clientcmd.Load(data)
		if err != nil {
			t.Fatal(err)
		}
		ctx := cfg.Contexts["lab"]
		if cfg.CurrentContext != "lab" || ctx == nil || ctx.Cluster != "lab" || ctx.AuthInfo != "lab" {
			t.Fatalf("via %q: contexts %v, current %q", tc.via, cfg.Contexts, cfg.CurrentContext)
		}
		cluster := cfg.Clusters["lab"]
		if cluster.Server != tc.server || cluster.TLSServerName != tc.serverName {
			t.Errorf("via %q: server %q for %q, want %q for %q", tc.via, cluster.Server, cluster.TLSServerName, tc.server, tc.serverName)
		}
		if string(cluster.CertificateAuthorityData) != secrets.Kubernetes.CABundle() {
			t.Errorf("via %q: CA %q, want the cluster's CA bundle", tc.via, cluster.CertificateAuthorityData)
		}
		block, _ := pem.Decode(cfg.AuthInfos["lab"].ClientCertificateData)
		if block == nil {
			t.Fatalf("via %q: no client certificate", tc.via)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if cert.Subject.CommonName != "alice" {
			t.Errorf("via %q: client certificate for %q, want alice", tc.via, cert.Subject.CommonName)
		}
		if got := cert.NotAfter.Sub(now); got > time.Hour+time.Minute || got < 59*time.Minute {
			t.Errorf("via %q: client certificate valid for %v, want an hour", tc.via, got)
		}
	}
}
