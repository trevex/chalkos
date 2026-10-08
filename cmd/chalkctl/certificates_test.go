package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd"
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
