package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
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

// trustOf returns the node's status's trust entries by name.
func trustOf(t *testing.T, s *Server) map[string]*nodev1.TrustStatus {
	t.Helper()
	trust := map[string]*nodev1.TrustStatus{}
	for _, tr := range status(t, s).Trust {
		trust[tr.Name] = tr
	}
	return trust
}

// TestStatusReportsKubernetesTrust checks that a control plane reports the CAs and keys its
// components run with, the issuing ones marked, and a worker the CAs its kubelet trusts.
func TestStatusReportsKubernetesTrust(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	secrets, err := pki.GenerateSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.BeginRotation(pki.RotateEncryptionKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	secrets.NodeCA = testNodeCA(t)
	share, err := kpki.ShareFor(&secrets, k8s.KindControlPlane, "n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := share.Encode()
	write(t, s.Kubernetes.Paths.Share(), string(data))
	if err := knode.Prepare(s.Kubernetes.Paths, time.Now(), nodeIP("192.168.100.11"), nil); err != nil {
		t.Fatal(err)
	}
	trust := trustOf(t, s)
	k := secrets.Kubernetes
	ca, _ := pki.ParseCertificate([]byte(k.CA.Certificate))
	for name, want := range map[string][]string{
		"Kubernetes CA":   {pki.Fingerprint(ca.Raw)},
		"front-proxy CA":  nil,
		"etcd CA":         nil,
		"encryption keys": {k.EncryptionKeys()[0].Fingerprint(), k.EncryptionKeys()[1].Fingerprint()},
	} {
		tr := trust[name]
		if tr == nil || len(tr.Fingerprints) == 0 || tr.Issuing != tr.Fingerprints[0] {
			t.Errorf("%s: %v, want the issuing value first and marked", name, tr)
			continue
		}
		if want != nil && !slices.Equal(tr.Fingerprints, want) {
			t.Errorf("%s: %v, want %v", name, tr.Fingerprints, want)
		}
	}
	pub, _ := pki.ServiceAccountPublicKey(k.ServiceAccountKey)
	fp, _ := pki.PublicKeyFingerprint(pub)
	if tr := trust["service-account keys"]; tr == nil || !slices.Equal(tr.Fingerprints, []string{fp}) || tr.Issuing != fp {
		t.Errorf("service-account keys: %v, want %s", tr, fp)
	}

	w, _ := kubernetesServer(t, k8s.KindWorker, true)
	workerShare, _ := knode.ReadShare(w.Kubernetes.Paths)
	want, _ := pki.Fingerprints(workerShare.CABundle())
	if tr := trustOf(t, w)["Kubernetes CA"]; tr == nil || !slices.Equal(tr.Fingerprints, want) || tr.Issuing != "" {
		t.Errorf("worker: Kubernetes CA %v, want %v and nothing issuing", tr, want)
	}
}

// TestStatusReportsTheControlPlanesState checks that a bootstrapped control plane says whether
// it runs on its current certificates.
func TestStatusReportsTheControlPlanesState(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	if err := knode.MarkBootstrapped(s.Kubernetes.Paths, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Kubernetes.ControlPlaneState = func(context.Context, k8s.Cluster, kpki.Share) string { return "current" }
	if got := status(t, s).Kubernetes.ControlPlane; got != "current" {
		t.Errorf("control plane %q", got)
	}
}

func TestServesFile(t *testing.T) {
	served, _ := pki.SelfSigned("served", time.Now())
	other, _ := pki.SelfSigned("other", time.Now())
	pair, _ := tls.X509KeyPair([]byte(served.Certificate), []byte(served.Key))
	addr := serveTLS(t, http.NotFoundHandler(), TLSConfig(StaticCertificate(&pair), nil))
	dir := t.TempDir()
	write(t, filepath.Join(dir, "served.crt"), served.Certificate)
	write(t, filepath.Join(dir, "other.crt"), other.Certificate)
	if err := servesFile(context.Background(), addr, filepath.Join(dir, "served.crt")); err != nil {
		t.Error(err)
	}
	if err := servesFile(context.Background(), addr, filepath.Join(dir, "other.crt")); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("another certificate: %v", err)
	}
	if err := servesFile(context.Background(), "127.0.0.1:1", filepath.Join(dir, "served.crt")); err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("nothing listening: %v", err)
	}
}

// TestCallsVerifyAgainstTheCurrentOSCAs checks that every call is authorised by the OS CAs the
// node trusts when the call arrives, not when its connection was made: a client of a root the
// node stopped trusting, or whose certificate expired, is refused on a connection it kept open.
func TestCallsVerifyAgainstTheCurrentOSCAs(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	withNodeCertificate(t, s)
	var skew atomic.Int64
	s.clock = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	osCA, _ := testOSCA()
	next, _ := pki.NewOSCA(time.Now())
	nextNodeCA, _ := pki.NewNodeCA(next, time.Now())
	oldClient, oldAdmin := adminOf(t, osCA)
	nextClient, nextAdmin := adminOf(t, next)
	both := pki.Bundle(osCA.Certificate, next.Certificate)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns atomic.Int32
	srv := &http.Server{
		Handler:   s.Handler(),
		TLSConfig: TLSConfig(s.Certificate.GetCertificate, s.Certificate.ClientCAs),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				conns.Add(1)
			}
		},
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	roots, _ := pki.BundlePool(both)
	open := func(cert *tls.Certificate) *client.Conn {
		c, err := client.Dial(ln.Addr().String(), client.Options{CA: roots, ServerName: "n1", Certificate: cert})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
	info := func(c *client.Conn) error {
		_, err := c.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
		return err
	}
	// Two connections of the old root's admin, each kept open.
	forInfo, forApply := open(oldClient), open(oldClient)
	for _, c := range []*client.Conn{forInfo, forApply} {
		if err := info(c); err != nil {
			t.Fatal(err)
		}
	}

	// The node moves to the new root alone, as the finish of a rotation does.
	if err := s.Certificate.ReplaceTrust(both, oldAdmin); err != nil {
		t.Fatal(err)
	}
	renewed, _ := pki.IssueNode(nextNodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err := s.Certificate.Replace(renewed.Certificate, renewed.Key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Certificate.ReplaceTrust(next.Certificate, nextAdmin); err != nil {
		t.Fatal(err)
	}
	opened := conns.Load()

	if err := info(forInfo); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Info on a connection of the old root: %v, want unauthenticated", err)
	}
	_, err = forApply.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{Trust: []byte(both)}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("ApplyIdentity on a connection of the old root: %v, want unauthenticated", err)
	}
	if got := s.Certificate.OSCA(); got != next.Certificate {
		t.Error("a client of the old root brought it back")
	}
	if got := conns.Load(); got != opened {
		t.Fatalf("the calls opened %d new connections; they were to reuse the old ones", got-opened)
	}
	if err := info(open(oldClient)); err == nil {
		t.Error("a new connection of the old root is served")
	}

	// A certificate that expires while its connection stays open is refused from then on.
	current := open(nextClient)
	if err := info(current); err != nil {
		t.Fatal(err)
	}
	skew.Store(int64(2 * time.Hour))
	if err := info(current); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Info with an expired certificate: %v, want unauthenticated", err)
	}
}

// TestTrustNotRecordedIsAnInternalError checks that OS CAs the node cannot write to STATE are
// reported as the node's failure, not as a bad request.
func TestTrustNotRecordedIsAnInternalError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only directories")
	}
	s, _ := installedServer(t, section("", ""), false)
	withNodeCertificate(t, s)
	osCA, _ := testOSCA()
	next, _ := pki.NewOSCA(time.Now())
	_, admin := adminOf(t, osCA)
	dir := filepath.Join(s.Paths.StateDir, "chalkd")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	_, err := s.ApplyIdentity(asCaller(admin), connect.NewRequest(&nodev1.ApplyIdentityRequest{Trust: []byte(pki.Bundle(osCA.Certificate, next.Certificate))}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("%v, want internal", err)
	}
	if s.Certificate.OSCA() != osCA.Certificate {
		t.Error("the node trusts OS CAs it did not record")
	}
}
