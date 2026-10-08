package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// adminOf issues an admin client certificate of the OS CA.
func adminOf(t *testing.T, osCA pki.CertKey) (*tls.Certificate, *x509.Certificate) {
	t.Helper()
	ck, err := pki.IssueClient(osCA, "chalkctl", pki.RoleAdmin, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	return &pair, leaf
}

// asCaller is the context of a call whose client certificate is cert.
func asCaller(cert *x509.Certificate) context.Context {
	return context.WithValue(context.Background(), peerKey{}, cert)
}

// TestApplyIdentityReplacesTheOSCAs checks that a node trusts the OS CAs delivered to it from
// then on, without a restart, and refuses ones that would lock it or its caller out.
func TestApplyIdentityReplacesTheOSCAs(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	withNodeCertificate(t, s)
	osCA, err := testOSCA()
	if err != nil {
		t.Fatal(err)
	}
	next, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, admin := adminOf(t, osCA)
	nextClient, nextAdmin := adminOf(t, next)
	both := pki.Bundle(osCA.Certificate, next.Certificate)
	leaf, _ := pki.IssueLeaf(next, pki.Leaf{CommonName: "leaf", Client: true}, time.Now())
	renewed, _ := pki.IssueNode(testNodeCA(t), pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())

	addr := serveTLS(t, s.Handler(), TLSConfig(s.Certificate.GetCertificate, s.Certificate.ClientCAs))
	roots, _ := pki.BundlePool(both)
	reach := func() error {
		c, err := client.Dial(addr, client.Options{CA: roots, ServerName: "n1", Certificate: nextClient})
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
		return err
	}
	if err := reach(); err == nil {
		t.Fatal("a client of an OS CA the node does not trust reached it")
	}

	for name, tc := range map[string]struct {
		caller *x509.Certificate
		req    *nodev1.ApplyIdentityRequest
	}{
		"the new OS CA alone, which the node's certificate does not chain to": {admin, &nodev1.ApplyIdentityRequest{Trust: []byte(next.Certificate)}},
		"OS CAs without the caller's":                                         {nextAdmin, &nodev1.ApplyIdentityRequest{Trust: []byte(osCA.Certificate)}},
		"a key":                                                               {admin, &nodev1.ApplyIdentityRequest{Trust: []byte(both + next.Key)}},
		"a leaf":                                                              {admin, &nodev1.ApplyIdentityRequest{Trust: []byte(both + leaf.Certificate)}},
		"a node CA as a root":                                                 {admin, &nodev1.ApplyIdentityRequest{Trust: []byte(both + testNodeCA(t).Certificate)}},
		"OS CAs with a node certificate":                                      {admin, &nodev1.ApplyIdentityRequest{Trust: []byte(both), NodeCertificate: []byte(renewed.Certificate), NodeKey: []byte(renewed.Key)}},
	} {
		_, err := s.ApplyIdentity(asCaller(tc.caller), connect.NewRequest(tc.req))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid_argument", name, err)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), "BEGIN") {
			t.Errorf("%s: the error holds PEM: %v", name, err)
		}
	}
	if s.Certificate.OSCA() != osCA.Certificate {
		t.Fatal("a refused request changed the OS CAs")
	}

	if _, err := s.ApplyIdentity(asCaller(admin), connect.NewRequest(&nodev1.ApplyIdentityRequest{Trust: []byte(both)})); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "chalkd", CAFile)); string(got) != both || s.Certificate.OSCA() != both {
		t.Errorf("the node trusts %q, want both OS CAs", got)
	}
	if len(r.calls) != 0 {
		t.Errorf("delivering OS CAs ran %v", r.calls)
	}
	if err := reach(); err != nil {
		t.Errorf("a client of the new OS CA is refused once it is trusted: %v", err)
	}
	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := pki.Fingerprints(both)
	var found bool
	for _, tr := range resp.Msg.Trust {
		if tr.Name == "OS CA" {
			found = slices.Equal(tr.Fingerprints, want)
		}
	}
	if !found {
		t.Errorf("status trust = %v, want the OS CAs %v", resp.Msg.Trust, want)
	}
	var osCAs int
	for _, c := range resp.Msg.Certificates {
		if c.Name == "OS CA" {
			osCAs++
		}
		if c.Name == "node" && c.Issuer == "" {
			t.Error("the node certificate's status names no issuer")
		}
	}
	if osCAs != 2 {
		t.Errorf("status lists %d OS CAs, want 2", osCAs)
	}

	// A restarted chalkd trusts what was delivered.
	again, err := LoadNodeCertificate(filepath.Join(s.Paths.StateDir, "chalkd"))
	if err != nil || again.OSCA() != both {
		t.Errorf("after a restart: %v", err)
	}
}

// TestTrustKeepsTheControlPlanesNodeCA checks that a control plane refuses OS CAs its node CA
// does not chain to, so it never renews node certificates nobody trusts.
func TestTrustKeepsTheControlPlanesNodeCA(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	withNodeCertificate(t, s)
	osCA, _ := testOSCA()
	next, _ := pki.NewOSCA(time.Now())
	nextNodeCA, _ := pki.NewNodeCA(next, time.Now())
	k, _ := pki.NewKubernetesSecrets(time.Now())
	share, _ := kpki.ControlPlaneShare(k, nextNodeCA).Encode()
	write(t, s.Kubernetes.Paths.Share(), string(share))
	_, admin := adminOf(t, osCA)
	if _, err := s.ApplyIdentity(asCaller(admin), connect.NewRequest(&nodev1.ApplyIdentityRequest{Trust: []byte(osCA.Certificate)})); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "node CA") {
		t.Errorf("OS CAs without the node CA's: %v, want a refusal naming the node CA", err)
	}
	if _, err := s.ApplyIdentity(asCaller(admin), connect.NewRequest(&nodev1.ApplyIdentityRequest{Trust: []byte(pki.Bundle(osCA.Certificate, next.Certificate))})); err != nil {
		t.Errorf("OS CAs with the node CA's: %v", err)
	}
}
