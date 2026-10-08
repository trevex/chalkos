package chalkd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// controlPlaneOf is a control-plane node whose share holds the node CA of c.
func controlPlaneOf(t *testing.T, c creds) *Server {
	t.Helper()
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := kpki.ControlPlaneShare(k, c.nodeCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	write(t, s.Kubernetes.Paths.Share(), string(data))
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
	resp, err := dial(t, addr, pair(t, w1)).RenewNodeCertificate(context.Background(), connect.NewRequest(&nodev1.RenewNodeCertificateRequest{CertificateRequest: request}))
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
	for name, tc := range map[string]struct {
		cert    *tls.Certificate
		request []byte
		code    connect.Code
	}{
		"an admin":                  {c.clients[pki.RoleAdmin], other, connect.CodePermissionDenied},
		"a reader":                  {c.clients[pki.RoleReader], other, connect.CodePermissionDenied},
		"no certificate":            {nil, other, connect.CodeUnavailable},
		"a request of another key":  {pair(t, w1), broken, connect.CodeInvalidArgument},
		"a request that is no PKCS": {pair(t, w1), []byte("not a request"), connect.CodeInvalidArgument},
	} {
		_, err := dial(t, addr, tc.cert).RenewNodeCertificate(context.Background(), connect.NewRequest(&nodev1.RenewNodeCertificateRequest{CertificateRequest: tc.request}))
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
	for name, s := range map[string]*Server{"a worker": worker, "a node without Kubernetes": plain} {
		addr := serve(t, s, c, c.pool)
		_, err := dial(t, addr, c.clients[pki.RoleNode]).RenewNodeCertificate(context.Background(), connect.NewRequest(&nodev1.RenewNodeCertificateRequest{CertificateRequest: request}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "not a control plane") {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
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
		return RenewThrough(ctx, w1, addr, request)
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
	if _, err := RenewThrough(context.Background(), w1, maintenanceAddr, request); err == nil {
		t.Error("asked a peer without a node certificate")
	}
}

// fakeClock hands the waits a Renewal asks for to the test, which ends each one.
type fakeClock struct {
	now   time.Time
	waits chan time.Duration
	fire  chan time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, waits: make(chan time.Duration), fire: make(chan time.Time)}
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.waits <- d
	return f.fire
}

// next returns the wait the renewal asked for, ends it at now and lets the renewal go on.
func (f *fakeClock) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.waits:
		return d
	case <-time.After(time.Minute):
		t.Fatal("the renewal asked for no wait")
		return 0
	}
}

func TestRenewalThresholdsAndBackoff(t *testing.T) {
	c := newCreds(t)
	start := time.Now()
	cert := loadedCertificate(t, c, c.node)
	leaf := cert.Current().Leaf
	clock := newFakeClock(start)
	failing := true
	issued := 0
	r := NewNodeRenewal(cert, func(_ context.Context, request []byte) (string, error) {
		issued++
		if failing {
			return "", errors.New("connection refused")
		}
		return signRequest(c.nodeCA, pki.NamesOf(leaf), request, clock.now)
	})
	r.Now = func() time.Time { return clock.now }
	r.After = clock.After
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		for {
			select {
			case <-done:
				return
			case <-clock.waits:
			case clock.fire <- clock.now:
			}
		}
	})
	advance := func(now time.Time) time.Duration {
		clock.now = now
		clock.fire <- now
		return clock.next(t)
	}

	if first := clock.next(t); first >= time.Hour/10 {
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
	// Once due, failures are tried again after a minute, doubling up to an hour.
	var retries []time.Duration
	now := renewAt
	for range 9 {
		retries = append(retries, advance(now))
		now = now.Add(retries[len(retries)-1])
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i := range want {
		if retries[i] != want[i] {
			t.Fatalf("waits after failures %v, want %v", retries, want)
		}
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
		"no earlier end": func(request []byte) (string, error) {
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
	r := NewNodeRenewal(s.Certificate, s.IssueNodeCertificate)
	before := s.Certificate.Fingerprint()
	if err := r.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Certificate.Fingerprint() == before {
		t.Error("the control plane did not renew its own certificate")
	}
}
