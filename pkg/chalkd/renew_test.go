package chalkd

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/chalkd/renewal/renewaltest"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
	"github.com/trevex/chalkos/pkg/pki"
)

// controlPlaneOf is a control-plane node whose share holds the node CA of c, in a cluster that
// revoked no node unless adjust says otherwise.
func controlPlaneOf(t *testing.T, c creds, adjust ...func(*kubernetesplugin.Options)) *Server {
	t.Helper()
	noneRevoked := func(o *kubernetesplugin.Options) {
		o.RevokedNodes = func(context.Context) (revoked.List, error) { return revoked.List{}, nil }
	}
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true, append([]func(*kubernetesplugin.Options){noneRevoked}, adjust...)...)
	k, err := clustersecrets.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := kpki.ControlPlaneShare(k, c.nodeCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	write(t, kubernetesPaths(s).Share(), string(data))
	return s
}

// certificateRequest returns a PKCS #10 request for a new key, with the names given, and the key.
func certificateRequest(t *testing.T, commonName string, dnsNames ...string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}, DNSNames: dnsNames}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func pair(t *testing.T, ck pki.CertKey) *tls.Certificate {
	t.Helper()
	p, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestRenewNodeCertificate(t *testing.T) {
	c := newCreds(t)
	addr := serve(t, controlPlaneOf(t, c), c, c.pool)
	names := pki.NodeNames{CommonName: "w1", DNSNames: []string{"w1", "w1.lan"}, IPs: []net.IP{net.ParseIP("10.0.0.21")}}
	w1, err := pki.IssueNode(c.nodeCA, names, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The request asks for other names; the certificate carries the caller's.
	request, key := certificateRequest(t, "cp1", "cp1", "kubernetes")
	resp, err := kubernetesClient(dial(t, addr, pair(t, w1))).RenewNodeCertificate(context.Background(), connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{CertificateRequest: request}))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(key)
	renewed, err := pki.VerifyNode(string(resp.Msg.CertificateChain), keyPEM, c.ca.Certificate, time.Now())
	if err != nil {
		t.Fatalf("the renewed certificate: %v", err)
	}
	if got := pki.NamesOf(renewed.Leaf); !got.Equal(names) {
		t.Errorf("renewed for %+v, want exactly %+v", got, names)
	}

	broken := append([]byte{}, request...)
	broken[len(broken)-1] ^= 0xff
	other, _ := certificateRequest(t, "w1")
	// Node keys are P-256; a request for any other key is refused.
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var notP256 [][]byte
	for _, k := range []crypto.Signer{p384, edKey} {
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "w1"}}, k)
		if err != nil {
			t.Fatal(err)
		}
		notP256 = append(notP256, der)
	}
	for name, tc := range map[string]struct {
		cert    *tls.Certificate
		request []byte
		code    connect.Code
	}{
		"an admin":                     {c.clients[pki.RoleAdmin], other, connect.CodePermissionDenied},
		"a reader":                     {c.clients[pki.RoleReader], other, connect.CodePermissionDenied},
		"no certificate":               {nil, other, connect.CodeUnavailable},
		"a request of another key":     {pair(t, w1), broken, connect.CodeInvalidArgument},
		"a request that is no PKCS":    {pair(t, w1), []byte("not a request"), connect.CodeInvalidArgument},
		"a request for a P-384 key":    {pair(t, w1), notP256[0], connect.CodeInvalidArgument},
		"a request for an Ed25519 key": {pair(t, w1), notP256[1], connect.CodeInvalidArgument},
	} {
		_, err := kubernetesClient(dial(t, addr, tc.cert)).RenewNodeCertificate(context.Background(), connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{CertificateRequest: tc.request}))
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", name, err, tc.code)
		}
	}
}

func TestRenewNodeCertificateOnControlPlanesOnly(t *testing.T) {
	c := newCreds(t)
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	plain, _ := installedServer(t, section("", ""), false)
	request, _ := certificateRequest(t, "n2")
	for name, tc := range map[string]struct {
		s    *Server
		code connect.Code
		want string
	}{
		"a worker":                  {worker, connect.CodeFailedPrecondition, "not a control plane"},
		"a node without Kubernetes": {plain, connect.CodeUnimplemented, "the kubernetes layer is not in this image"},
	} {
		addr := serve(t, tc.s, c, c.pool)
		_, err := kubernetesClient(dial(t, addr, c.clients[pki.RoleNode])).RenewNodeCertificate(context.Background(), connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{CertificateRequest: request}))
		if connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %v %q", name, err, tc.code, tc.want)
		}
	}
}

// renewAs asks the chalkd at addr to renew a node certificate of the node CA of c for name.
func renewAs(t *testing.T, c creds, addr, name string) error {
	t.Helper()
	cert, err := pki.IssueNode(c.nodeCA, pki.NodeNames{CommonName: name, DNSNames: []string{name}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	request, _ := certificateRequest(t, name)
	_, err = kubernetesClient(dial(t, addr, pair(t, cert))).RenewNodeCertificate(context.Background(), connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{CertificateRequest: request}))
	return err
}

// TestRenewNodeCertificateOfRevokedNode refuses to renew the certificate of a revoked node, and
// every certificate while the revoked nodes cannot be read.
func TestRenewNodeCertificateOfRevokedNode(t *testing.T) {
	c := newCreds(t)
	revokedAt := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	var loadErr error
	s := controlPlaneOf(t, c, func(o *kubernetesplugin.Options) {
		o.RevokedNodes = func(context.Context) (revoked.List, error) {
			return revoked.List{"w1": revokedAt, "w3": {}}, loadErr
		}
	})
	addr := serve(t, s, c, c.pool)
	for name, want := range map[string]string{
		"w1": "node w1 is revoked since 2026-10-23T08:00:00Z; chalkctl node unrevoke w1 lets it renew again",
		"w3": "node w3 is revoked; chalkctl node unrevoke w3 lets it renew again",
	} {
		if err := renewAs(t, c, addr, name); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), want) {
			t.Errorf("renewing %s: %v, want %q", name, err, want)
		}
	}
	if err := renewAs(t, c, addr, "w2"); err != nil {
		t.Errorf("renewing w2: %v", err)
	}
	loadErr = errors.New("connection refused")
	if err := renewAs(t, c, addr, "w2"); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("renewing w2 without the revoked nodes: %v, want a refusal", err)
	}
}

// TestRenewNodeCertificateReadsRevokedNodes reads the revoked nodes through the control plane's
// own API server, where RevokeNode lists them.
func TestRenewNodeCertificateReadsRevokedNodes(t *testing.T) {
	c := newCreds(t)
	cs := fake.NewClientset()
	s := controlPlaneOf(t, c, func(o *kubernetesplugin.Options) {
		o.RevokedNodes = nil
		o.Hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return cs, nil }
	})
	write(t, kubernetesPaths(s).Bootstrapped(), "")
	if _, err := kubernetesOf(s).RevokeNode(context.Background(), connect.NewRequest(&kubernetesv1.RevokeNodeRequest{Node: "w1"})); err != nil {
		t.Fatal(err)
	}
	addr := serve(t, s, c, c.pool)
	if err := renewAs(t, c, addr, "w1"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("renewing w1: %v, want PermissionDenied", err)
	}
	if err := renewAs(t, c, addr, "w2"); err != nil {
		t.Errorf("renewing w2: %v", err)
	}
	// Without its node file, the node cannot reach its API server; the refusal keeps its own code.
	if err := os.Remove(kubernetesPaths(s).NodeFile); err != nil {
		t.Fatal(err)
	}
	err := renewAs(t, c, addr, "w2")
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "unavailable") {
		t.Errorf("renewing w2 without the node file: %v, want an internal error alone", err)
	}
}

// TestIssueNodeCertificateOfRevokedControlPlane refuses a control plane its own certificate once
// it is revoked, as one removed from etcd without leaving it would be.
func TestIssueNodeCertificateOfRevokedControlPlane(t *testing.T) {
	c := newCreds(t)
	var loadErr error
	s := controlPlaneOf(t, c, func(o *kubernetesplugin.Options) {
		o.RevokedNodes = func(context.Context) (revoked.List, error) { return revoked.List{"n1": time.Now()}, loadErr }
	})
	issued, err := pki.IssueNode(c.nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s.Certificate = loadedCertificate(t, c, issued)
	r := NewNodeRenewal(s.Certificate, kubernetesOf(s).IssueNodeCertificate)
	before := s.Certificate.Fingerprint()
	if err := r.Renew(context.Background(), time.Now()); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "node n1 is revoked") {
		t.Errorf("renewing n1: %v, want a refusal", err)
	}
	loadErr = errors.New("connection refused")
	if err := r.Renew(context.Background(), time.Now()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("renewing n1 without the revoked nodes: %v, want Unavailable", err)
	}
	if s.Certificate.Fingerprint() != before {
		t.Error("the refused renewals replaced the certificate")
	}
}

// loadedCertificate is the node certificate store of a node with the certificate.
func loadedCertificate(t *testing.T, c creds, cert pki.CertKey) *NodeCertificate {
	t.Helper()
	c.node = cert
	n, err := LoadNodeCertificate(nodeCertificateDir(t, c))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorkerRenewsThroughControlPlane(t *testing.T) {
	c := newCreds(t)
	addr := serve(t, controlPlaneOf(t, c), c, c.pool)
	names := pki.NodeNames{CommonName: "w1", DNSNames: []string{"w1"}, IPs: []net.IP{net.ParseIP("10.0.0.21")}}
	// Issued a while ago, so the renewed certificate ends later.
	issued, err := pki.IssueNode(c.nodeCA, names, time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	w1 := loadedCertificate(t, c, issued)
	before := w1.Fingerprint()
	r := NewNodeRenewal(w1, func(ctx context.Context, request []byte) (string, error) {
		return kubernetesplugin.RenewThrough(ctx, w1, addr, request)
	})
	if err := r.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if w1.Fingerprint() == before || !pki.NamesOf(w1.Current().Leaf).Equal(names) {
		t.Errorf("serves %+v after the renewal", pki.NamesOf(w1.Current().Leaf))
	}

	// A peer that is no node, such as a chalkd in maintenance mode, is not asked.
	maintenanceNode, _ := newTestServer(t, maintenance, vda)
	maintenanceAddr := serve(t, maintenanceNode, c, nil)
	request, _ := certificateRequest(t, "w1")
	if _, err := kubernetesplugin.RenewThrough(context.Background(), w1, maintenanceAddr, request); err == nil {
		t.Error("asked a peer without a node certificate")
	}
}

func TestRenewalThresholdsAndBackoff(t *testing.T) {
	c := newCreds(t)
	start := time.Now()
	cert := loadedCertificate(t, c, c.node)
	leaf := cert.Current().Leaf
	clock := renewaltest.NewClock(start)
	failing := true
	issued := 0
	r := NewNodeRenewal(cert, func(_ context.Context, request []byte) (string, error) {
		issued++
		if failing {
			return "", errors.New("connection refused")
		}
		return signRequest(c.nodeCA, pki.NamesOf(leaf), request, clock.Now())
	})
	r.Now = clock.Now
	r.After = clock.After
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		clock.Drain(done)
	})
	advance := func(now time.Time) time.Duration { return clock.Advance(t, now) }

	if first := clock.Next(t); first >= time.Hour/10 {
		t.Errorf("the first check comes after %v, want soon after the start", first)
	}
	// Before two thirds of the lifetime nothing is renewed, and the next check is about an hour on.
	renewAt := pki.RenewAt(leaf)
	wait := advance(renewAt.Add(-time.Minute))
	if issued != 0 {
		t.Error("renewed before two thirds of the lifetime passed")
	}
	if wait < 54*time.Minute || wait > 66*time.Minute {
		t.Errorf("the next check comes after %v, want about an hour", wait)
	}
	// Once due, failures are tried again after about a minute, doubling up to about an hour; each
	// wait is spread by up to a tenth either way, so nodes that failed together do not retry together.
	var retries []time.Duration
	now := renewAt
	for range 9 {
		retries = append(retries, advance(now))
		now = now.Add(retries[len(retries)-1])
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i := range want {
		if retries[i] < want[i]-want[i]/10 || retries[i] > want[i]+want[i]/10 {
			t.Fatalf("waits after failures %v, want about %v", retries, want)
		}
	}
	if slices.Equal(retries, want) {
		t.Errorf("waits after failures %v carry no jitter", retries)
	}
	problem := r.Problem()
	if !strings.HasPrefix(problem, "renewal failing: connection refused; expires ") || !strings.Contains(problem, leaf.NotAfter.UTC().Format(time.RFC3339)) {
		t.Errorf("problem = %q", problem)
	}
	if cert.Current().Leaf != leaf {
		t.Error("a failed renewal replaced the certificate")
	}
	failing = false
	if wait := advance(now); wait < 54*time.Minute {
		t.Errorf("after the renewal the next check comes after %v", wait)
	}
	if r.Problem() != "" || cert.Current().Leaf.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
		t.Errorf("after a renewal: problem %q, serial unchanged %v", r.Problem(), cert.Current().Leaf.SerialNumber.Cmp(leaf.SerialNumber) == 0)
	}
}

func TestRenewalRefusesWhatItDidNotAskFor(t *testing.T) {
	c := newCreds(t)
	cert := loadedCertificate(t, c, c.node)
	leaf := cert.Current().Leaf
	other := newCreds(t)
	for name, issue := range map[string]func(request []byte) (string, error){
		"other names": func(request []byte) (string, error) {
			return signRequest(c.nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1", "kubernetes"}}, request, time.Now())
		},
		"another key": func([]byte) (string, error) {
			ck, err := pki.IssueNode(c.nodeCA, pki.NamesOf(leaf), time.Now())
			return ck.Certificate, err
		},
		"another OS CA": func(request []byte) (string, error) {
			return signRequest(other.nodeCA, pki.NamesOf(leaf), request, time.Now())
		},
		"no later end": func(request []byte) (string, error) {
			return signRequest(c.nodeCA, pki.NamesOf(leaf), request, leaf.NotBefore.Add(time.Hour))
		},
		"garbage": func([]byte) (string, error) { return "garbage", nil },
	} {
		r := NewNodeRenewal(cert, func(_ context.Context, request []byte) (string, error) { return issue(request) })
		if err := r.Renew(context.Background(), time.Now()); err == nil {
			t.Errorf("%s: renewed", name)
		}
		if cert.Current().Leaf != leaf {
			t.Errorf("%s: replaced the certificate", name)
		}
	}
}

func TestIssueNodeCertificateLocallyOnControlPlanes(t *testing.T) {
	c := newCreds(t)
	s := controlPlaneOf(t, c)
	issued, err := pki.IssueNode(c.nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s.Certificate = loadedCertificate(t, c, issued)
	r := NewNodeRenewal(s.Certificate, kubernetesOf(s).IssueNodeCertificate)
	before := s.Certificate.Fingerprint()
	if err := r.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Certificate.Fingerprint() == before {
		t.Error("the control plane did not renew its own certificate")
	}
}

// TestRenewOnApplyConfig checks the test images' hook: with it, ApplyConfig renews the node
// certificate though it is not due; without it, as on every other image, it does not.
func TestRenewOnApplyConfig(t *testing.T) {
	for _, hook := range []bool{false, true} {
		s, _ := installedServer(t, section("", ""), false)
		withNodeCertificate(t, s)
		s.RenewOnApplyConfig = hook
		issued := make(chan struct{}, 1)
		s.Renewal = NewNodeRenewal(s.Certificate, func(context.Context, []byte) (string, error) {
			issued <- struct{}{}
			return "", errors.New("the test issues none")
		})
		// The clock never ends a wait: only a forced renewal runs.
		s.Renewal.After = func(time.Duration) <-chan time.Time { return nil }
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { s.Renewal.Run(ctx); close(done) }()
		// OS CAs alone, as a rotation delivers them, renew nothing.
		if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Trust: []byte(s.Certificate.OSCA())})); err != nil {
			t.Fatal(err)
		}
		select {
		case <-issued:
			t.Error("delivering OS CAs alone renewed the node certificate")
		case <-time.After(100 * time.Millisecond):
		}
		recorded, _ := readIdentity(s)
		if _, err := deliver(context.Background(), s, string(recorded), &nodev1.ApplyConfigRequest{}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-issued:
			if !hook {
				t.Error("ApplyConfig renewed the node certificate without the test hook")
			}
		case <-time.After(time.Second):
			if hook {
				t.Error("ApplyConfig did not renew the node certificate with the test hook")
			}
		}
		cancel()
		<-done
	}
}

// silentPeer completes TLS handshakes as the node of cert and then never answers, until the
// client closes the connection.
func silentPeer(t *testing.T, cert pki.CertKey) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{*pair(t, cert)}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRenewalAttemptsEndAtTheirDeadline(t *testing.T) {
	c := newCreds(t)
	addr := silentPeer(t, c.node)
	issued, err := pki.IssueNode(c.nodeCA, pki.NodeNames{CommonName: "w1", DNSNames: []string{"w1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w1 := loadedCertificate(t, c, issued)
	r := NewNodeRenewal(w1, func(ctx context.Context, request []byte) (string, error) {
		return kubernetesplugin.RenewThrough(ctx, w1, addr, request)
	})
	r.Timeout = time.Second
	clock := renewaltest.NewClock(time.Now())
	r.Now = clock.Now
	r.After = clock.After

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	clock.Next(t)
	r.Force()
	start := time.Now()
	select {
	case <-clock.Waits():
	case <-time.After(10 * time.Second):
		t.Fatalf("an attempt with a silent peer still runs after %v", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the attempt ended after %v, want about its deadline of %v", elapsed, r.Timeout)
	}
	if !strings.HasPrefix(r.Problem(), "renewal failing: ") {
		t.Errorf("problem = %q", r.Problem())
	}
	cancel()
	<-done

	// The attempt's connection and the goroutines serving it are gone.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before the attempt, %d after", before, after)
	}
}

// On a control plane whose loop runs, the test images' hook renews the control plane's
// certificates after ApplyConfig too.
func TestRenewOnApplyConfigRenewsTheControlPlane(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	if _, err := kubernetestest.Bootstrap(context.Background(), kubernetesOf(s)); err != nil {
		t.Fatal(err)
	}
	s.RenewOnApplyConfig = true
	apiServer := filepath.Join(kubernetesPaths(s).PKI, kpki.FileAPIServer)
	before, err := os.ReadFile(apiServer)
	if err != nil {
		t.Fatal(err)
	}
	recorded, _ := readIdentity(s)
	if _, err := deliver(context.Background(), s, string(recorded), &nodev1.ApplyConfigRequest{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the control plane's certificates to be renewed", func() bool {
		after, _ := os.ReadFile(apiServer)
		return len(after) > 0 && string(after) != string(before)
	})
}

// A certificate delivered while a renewal runs, as by chalkctl node renew, stays: the renewal
// ends without replacing it and without failing.
func TestRenewalKeepsACertificateDeliveredMeanwhile(t *testing.T) {
	c := newCreds(t)
	names := pki.NodeNames{CommonName: "w1", DNSNames: []string{"w1"}}
	issued, err := pki.IssueNode(c.nodeCA, names, time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cert := loadedCertificate(t, c, issued)
	delivered, err := pki.IssueNode(c.nodeCA, names, time.Now().Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r := NewNodeRenewal(cert, func(_ context.Context, request []byte) (string, error) {
		if err := cert.Replace(delivered.Certificate, delivered.Key, time.Now()); err != nil {
			t.Fatal(err)
		}
		return signRequest(c.nodeCA, names, request, time.Now())
	})
	if err := r.Renew(context.Background(), time.Now()); err != nil {
		t.Errorf("the renewal failed: %v", err)
	}
	want, _ := pki.ParseCertificate([]byte(delivered.Certificate))
	if got := cert.Fingerprint(); got != pki.Fingerprint(want.Raw) {
		t.Error("the renewal replaced the certificate delivered while it ran")
	}
	reloaded, err := LoadNodeCertificate(filepath.Dir(cert.path))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Fingerprint() != pki.Fingerprint(want.Raw) {
		t.Error("STATE holds another certificate than the one delivered")
	}
}

// A control plane that left etcd is no control plane of the cluster any more: it signs no node
// certificate with the node CA its share still holds.
func TestRenewNodeCertificateRefusedOnceLeft(t *testing.T) {
	c := newCreds(t)
	s := controlPlaneOf(t, c)
	if err := knode.MarkLeft(kubernetesPaths(s), time.Now()); err != nil {
		t.Fatal(err)
	}
	addr := serve(t, s, c, c.pool)
	request, _ := certificateRequest(t, "n2")
	_, err := kubernetesClient(dial(t, addr, c.clients[pki.RoleNode])).RenewNodeCertificate(context.Background(), connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{CertificateRequest: request}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "left etcd") {
		t.Errorf("%v, want a refusal naming that the node left etcd", err)
	}
}

// signRequest issues a node certificate of nodeCA for the key of a PKCS #10 request, for the
// names given, as a control plane does.
func signRequest(nodeCA pki.CertKey, names pki.NodeNames, request []byte, now time.Time) (string, error) {
	csr, err := x509.ParseCertificateRequest(request)
	if err != nil {
		return "", err
	}
	return pki.SignNode(nodeCA, names, csr.PublicKey.(*ecdsa.PublicKey), now)
}
