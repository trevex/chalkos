package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// nodeWithCertificate serves s with the node certificate in a store it can replace.
func (ta *testApp) nodeWithCertificate(t *testing.T, s *chalkd.Server, cert pki.CertKey) string {
	t.Helper()
	dir := filepath.Join(s.Paths.StateDir, "chalkd")
	writeFile(t, filepath.Join(dir, chalkd.NodeCertificateFile), cert.Certificate+cert.Key)
	writeFile(t, filepath.Join(dir, chalkd.CAFile), ta.secrets.OSCA.Certificate)
	var err error
	if s.Certificate, err = chalkd.LoadNodeCertificate(dir); err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return serveTLS(t, s.Handler(), chalkd.TLSConfig(s.Certificate.GetCertificate, pool))
}

func TestNodeRenewReachesAnExpiredNode(t *testing.T) {
	// Secrets from two years ago, whose node CA issued a certificate that expired since.
	past := time.Now().Add(-2 * pki.LeafValidity)
	ta := newTestAppAt(t, past)
	expired, err := pki.IssueNode(ta.secrets.NodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, past)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, r := installedNode(t, ta, current)
	addr := ta.nodeWithCertificate(t, s, expired)

	// Every other command verifies the node's certificate with its dates.
	for _, cmd := range [][]string{{"status", "n1"}, {"apply-identity", "n1"}, {"reboot", "n1"}} {
		if err := ta.run(context.Background(), ta.args(cmd, addr)); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Errorf("%s reached a node whose certificate expired: %v", cmd[0], err)
		}
	}
	if err := ta.run(context.Background(), ta.args([]string{"node", "renew", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "n1 serves a new node certificate") {
		t.Errorf("stdout = %q", ta.stdout)
	}
	leaf := s.Certificate.Current().Leaf
	if !leaf.NotAfter.After(time.Now().Add(pki.LeafValidity - time.Hour)) {
		t.Errorf("the new certificate expires %v", leaf.NotAfter)
	}
	if len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != "10.0.0.11" {
		t.Errorf("the new certificate is for %v, want the node's static address", leaf.IPAddresses)
	}
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Errorf("status after the renewal: %v", err)
	}
	// Only the certificate was delivered: the identity and storage stayed as they were.
	for _, c := range r.calls {
		if !strings.HasPrefix(c, "systemctl list-units") {
			t.Errorf("node renew ran %s", c)
		}
	}
}

func TestNodeRenewVerifiesTheNode(t *testing.T) {
	ta := newTestApp(t)
	other := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, _ := installedNode(t, ta, current)
	// A node certificate of another cluster's node CA, for the right name.
	foreign, err := pki.IssueNode(other.secrets.NodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := other.nodeWithCertificate(t, s, foreign)
	if err := ta.run(context.Background(), ta.args([]string{"node", "renew", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "unknown authority") {
		t.Errorf("err = %v, want the node of another OS CA refused", err)
	}
}

func TestNodeCARotate(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, manifest.KindControlPlane)
	s, r := kubernetesNode(t, ta)
	p := s.Kubernetes.Paths
	writeFile(t, p.Cluster, `{"kind": "controlplane", "endpoint": "https://10.0.0.10:6443",
	  "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
	  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local"}`)
	share, err := kpki.ControlPlaneShare(&ta.secrets.Kubernetes, ta.secrets.NodeCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.Share(), string(share))
	writeFile(t, filepath.Join(s.Paths.StateDir, "chalkd", chalkd.CAFile), ta.secrets.OSCA.Certificate)
	addr := ta.startNode(t, s)

	out := filepath.Join(ta.dir, "secrets.rotated.json")
	pub := filepath.Join(ta.dir, "secrets.rotated.pub.json")
	args := ta.args([]string{"node-ca", "rotate", "--plaintext", "--out", out, "--public-out", pub}, addr)
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := pki.ReadSecrets(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.NodeCA == ta.secrets.NodeCA {
		t.Error("the node CA is the old one")
	}
	if rotated.OSCA != ta.secrets.OSCA || string(rotated.RecoverySecret) != string(ta.secrets.RecoverySecret) || rotated.Kubernetes.CA != ta.secrets.Kubernetes.CA {
		t.Error("the rotation changed more than the node CA")
	}
	var public pki.Public
	pubData, _ := os.ReadFile(pub)
	if err := json.Unmarshal(pubData, &public); err != nil || public.NodeCA.Certificate != rotated.NodeCA.Certificate || strings.Contains(string(pubData), "PRIVATE") {
		t.Errorf("public file: %v", err)
	}
	onNode, err := os.ReadFile(p.Share())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := kpki.ParseShare(onNode); err != nil || got.NodeCA == nil || *got.NodeCA != rotated.NodeCA {
		t.Errorf("the node's share does not hold the new node CA: %v", err)
	}
	if !strings.Contains(ta.stdout.String(), "n1 renews node certificates with the new node CA") {
		t.Errorf("stdout = %q", ta.stdout)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "systemctl restart") {
			t.Errorf("a new node CA restarted %s", c)
		}
	}

	// The secrets file is never overwritten.
	if err := ta.run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("err = %v, want a refusal to overwrite", err)
	}
	if err := ta.run(context.Background(), ta.args([]string{"node-ca", "rotate", "--plaintext"}, addr)); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %v, want --out required", err)
	}
}
