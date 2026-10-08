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

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
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
	r := NewNodeRenewal(s.Certificate, s.IssueNodeCertificate)
	before := s.Certificate.Fingerprint()
	if err := r.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Certificate.Fingerprint() == before {
		t.Error("the control plane did not renew its own certificate")
	}
}

// TestRenewOnApplyIdentity checks the test images' hook: with it, ApplyIdentity renews the node
// certificate though it is not due; without it, as on every other image, it does not.
func TestRenewOnApplyIdentity(t *testing.T) {
	for _, hook := range []bool{false, true} {
		s, _ := installedServer(t, section("", ""), false)
		withNodeCertificate(t, s)
		s.RenewOnApplyIdentity = hook
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
		recorded, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
		if _, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: string(recorded)})); err != nil {
			t.Fatal(err)
		}
		select {
		case <-issued:
			if !hook {
				t.Error("ApplyIdentity renewed the node certificate without the test hook")
			}
		case <-time.After(time.Second):
			if hook {
				t.Error("ApplyIdentity did not renew the node certificate with the test hook")
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
		return RenewThrough(ctx, w1, addr, request)
	})
	r.Timeout = time.Second
	clock := newFakeClock(time.Now())
	r.Now = func() time.Time { return clock.now }
	r.After = clock.After

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	clock.next(t)
	r.Force()
	start := time.Now()
	select {
	case <-clock.waits:
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
// certificates after ApplyIdentity too.
func TestRenewOnApplyIdentityRenewsTheControlPlane(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	if _, err := bootstrap(s, context.Background()); err != nil {
		t.Fatal(err)
	}
	s.RenewOnApplyIdentity = true
	apiServer := filepath.Join(s.Kubernetes.Paths.PKI, kpki.FileAPIServer)
	before, err := os.ReadFile(apiServer)
	if err != nil {
		t.Fatal(err)
	}
	recorded, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
	if _, err := s.ApplyIdentity(context.Background(), connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: string(recorded)})); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the control plane's certificates to be renewed", func() bool {
		after, _ := os.ReadFile(apiServer)
		return len(after) > 0 && string(after) != string(before)
	})
}

// runRenewal runs r on a fake clock until the test ends and returns the clock and a function
// that ends the current wait at the time given and returns the next wait.
func runRenewal(t *testing.T, r *Renewal, start time.Time) (*fakeClock, func(time.Time) time.Duration) {
	t.Helper()
	clock := newFakeClock(start)
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
	clock.next(t)
	return clock, func(now time.Time) time.Duration {
		clock.now = now
		clock.fire <- now
		return clock.next(t)
	}
}

// A check that fails, as before the preparation wrote the control plane's certificates at boot,
// renews nothing: the next check decides again whether anything is due.
func TestRenewalChecksAgainAfterAFailedCheck(t *testing.T) {
	start := time.Now()
	checks, renewals := 0, 0
	r := newRenewal("test", func(time.Time) (bool, time.Time, error) {
		checks++
		if checks == 1 {
			return false, time.Time{}, errors.New("no certificates yet")
		}
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error {
		renewals++
		return nil
	})
	_, advance := runRenewal(t, r, start)
	if wait := advance(start); wait > 2*time.Minute {
		t.Errorf("after a failed check the next one comes after %v", wait)
	}
	if !strings.HasPrefix(r.Problem(), "renewal failing: no certificates yet") {
		t.Errorf("problem after a failed check = %q", r.Problem())
	}
	if wait := advance(start.Add(time.Minute)); wait < 54*time.Minute {
		t.Errorf("with nothing due the next check comes after %v", wait)
	}
	if renewals != 0 {
		t.Errorf("renewed %d times though nothing was due", renewals)
	}
	if r.Problem() != "" {
		t.Errorf("problem once nothing is due = %q", r.Problem())
	}
}

// A renewal that failed is tried again only while it is still due: something else, such as a
// preparation or a delivered certificate, may have renewed in between.
func TestRenewalAfterAFailureRenewsOnlyWhileDue(t *testing.T) {
	start := time.Now()
	due, renewals := true, 0
	r := newRenewal("test", func(time.Time) (bool, time.Time, error) {
		if due {
			return true, start.Add(time.Hour), nil
		}
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error {
		renewals++
		return errors.New("busy")
	})
	_, advance := runRenewal(t, r, start)
	advance(start)
	if renewals != 1 || r.Problem() == "" {
		t.Fatalf("renewals %d, problem %q", renewals, r.Problem())
	}
	due = false
	advance(start.Add(time.Minute))
	if renewals != 1 {
		t.Errorf("renewed again though nothing was due any more")
	}
	if r.Problem() != "" {
		t.Errorf("problem once nothing is due = %q", r.Problem())
	}
}

// A renewal started again keeps no problem from an earlier run once a check finds nothing due.
func TestRenewalClearsAnEarlierProblem(t *testing.T) {
	start := time.Now()
	r := newRenewal("test", func(time.Time) (bool, time.Time, error) {
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error { return nil })
	r.setProblem("renewal failing: connection refused; expires 2027-10-08T12:00:00Z")
	_, advance := runRenewal(t, r, start)
	advance(start)
	if r.Problem() != "" {
		t.Errorf("problem once nothing is due = %q", r.Problem())
	}
}

// After a reboot chalkd's loops start before the preparation wrote the control plane's
// certificates: the renewal waits for them and issues none of its own.
func TestControlPlaneRenewalWaitsForThePreparation(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	withNodeCertificate(t, s)
	p := s.Kubernetes.Paths
	// RUN is empty after a reboot.
	if err := os.RemoveAll(p.Run); err != nil {
		t.Fatal(err)
	}
	r := s.Kubernetes.controlPlaneRenewal()
	renew := r.Renew
	renewals := 0
	r.Renew = func(ctx context.Context, now time.Time) error {
		renewals++
		return renew(ctx, now)
	}
	s.Kubernetes.leaves = r
	start := time.Now()
	_, advance := runRenewal(t, r, start)
	now := start
	for range 3 {
		now = now.Add(advance(now))
		if r.Problem() != "" {
			t.Errorf("problem before the preparation = %q", r.Problem())
		}
		for _, c := range status(t, s).Certificates {
			if strings.Contains(c.Problem, "renewal failing") {
				t.Errorf("status %s: %s", c.Name, c.Problem)
			}
		}
	}
	if exists(p.PKI) {
		t.Error("the renewal wrote the control plane's certificates before the preparation")
	}
	// The certificates the preparation writes are new: none are renewed, so no static pod
	// starts again.
	if err := knode.Prepare(p, now, nodeIP("192.168.100.11"), nil); err != nil {
		t.Fatal(err)
	}
	apiServer := filepath.Join(p.PKI, kpki.FileAPIServer)
	prepared, err := os.ReadFile(apiServer)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		now = now.Add(advance(now))
	}
	if renewals != 0 {
		t.Errorf("renewed %d times after the preparation", renewals)
	}
	if after, _ := os.ReadFile(apiServer); string(after) != string(prepared) {
		t.Error("the certificates the preparation wrote were issued again")
	}
	if r.Problem() != "" {
		t.Errorf("problem after the preparation = %q", r.Problem())
	}
}

// A forced renewal that failed is tried again until it succeeds, whether due or not.
func TestForcedRenewalIsTriedAgain(t *testing.T) {
	start := time.Now()
	failing, renewals := true, 0
	r := newRenewal("test", func(time.Time) (bool, time.Time, error) {
		return false, start.Add(48 * time.Hour), nil
	}, func(context.Context, time.Time) error {
		renewals++
		if failing {
			return errors.New("busy")
		}
		return nil
	})
	clock, advance := runRenewal(t, r, start)
	r.Force()
	if wait := clock.next(t); wait > 2*time.Minute || renewals != 1 {
		t.Fatalf("after a failed forced renewal: %d renewals, next attempt after %v", renewals, wait)
	}
	failing = false
	if wait := advance(start.Add(time.Minute)); wait < 54*time.Minute || renewals != 2 {
		t.Errorf("after a forced renewal: %d renewals, next check after %v", renewals, wait)
	}
	advance(start.Add(time.Hour))
	if renewals != 2 || r.Problem() != "" {
		t.Errorf("after the forced renewal succeeded: %d renewals, problem %q", renewals, r.Problem())
	}
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
	if err := knode.MarkLeft(s.Kubernetes.Paths, time.Now()); err != nil {
		t.Fatal(err)
	}
	addr := serve(t, s, c, c.pool)
	request, _ := certificateRequest(t, "n2")
	_, err := dial(t, addr, c.clients[pki.RoleNode]).RenewNodeCertificate(context.Background(), connect.NewRequest(&nodev1.RenewNodeCertificateRequest{CertificateRequest: request}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "left etcd") {
		t.Errorf("%v, want a refusal naming that the node left etcd", err)
	}
}
