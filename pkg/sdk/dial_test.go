package sdk_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/sdk"
)

func TestDialMaintenanceTrustsOnFirstUse(t *testing.T) {
	addr, fp := serveMaintenance(t)
	conn, info, got, err := sdk.DialMaintenance(context.Background(), addr, sdk.Trust{Insecure: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got != fp {
		t.Errorf("fingerprint %s, want %s", got, fp)
	}
	if info.Mode != nodev1.Mode_MODE_MAINTENANCE {
		t.Errorf("mode %s", info.Mode)
	}
}

// TestDialMaintenanceRefusesAnotherFingerprint checks that a fingerprint that is not the node's
// fails for the fingerprint alone, not for a reason DialMaintenance's other errors map to.
func TestDialMaintenanceRefusesAnotherFingerprint(t *testing.T) {
	addr, _ := serveMaintenance(t)
	_, _, _, err := sdk.DialMaintenance(context.Background(), addr, sdk.Trust{Fingerprint: strings.Repeat("00", 32)}, nil)
	if err == nil {
		t.Fatal("dialled a node of another fingerprint")
	}
	if errors.Is(err, sdk.ErrOSCARefused) {
		t.Errorf("err %v, want it not mapped to ErrOSCARefused", err)
	}
	if errors.Is(err, sdk.ErrInstalled) {
		t.Errorf("err %v, want it not mapped to ErrInstalled", err)
	}
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("err %v, want it to mention the fingerprint", err)
	}
}

func TestDialMaintenanceRefusesAnInstalledNode(t *testing.T) {
	addr, _, _ := serveNormal(t)
	conn, info, _, err := sdk.DialMaintenance(context.Background(), addr, sdk.Trust{Insecure: true}, nil)
	if !errors.Is(err, sdk.ErrInstalled) {
		t.Fatalf("err %v, want ErrInstalled", err)
	}
	if conn != nil {
		t.Error("ErrInstalled with a connection")
	}
	if info == nil || info.Mode != nodev1.Mode_MODE_NORMAL {
		t.Errorf("info = %v, want it returned with mode normal", info)
	}
}

// TestDialMaintenanceRefusesAnInstalledNodeByFingerprint checks that an explicit fingerprint,
// not only trust-on-first-use, still reports ErrInstalled with the node's Info.
func TestDialMaintenanceRefusesAnInstalledNodeByFingerprint(t *testing.T) {
	addr, fp, _ := serveNormal(t)
	conn, info, got, err := sdk.DialMaintenance(context.Background(), addr, sdk.Trust{Fingerprint: fp}, nil)
	if !errors.Is(err, sdk.ErrInstalled) {
		t.Fatalf("err %v, want ErrInstalled", err)
	}
	if conn != nil {
		t.Error("ErrInstalled with a connection")
	}
	if info == nil || info.Mode != nodev1.Mode_MODE_NORMAL {
		t.Errorf("info = %v, want it returned with mode normal", info)
	}
	if got != fp {
		t.Errorf("fingerprint %s, want %s", got, fp)
	}
}

func TestDialByOSCAChecksTheName(t *testing.T) {
	addr, _, secrets := serveNormal(t)
	cert, err := sdk.AdminCertificate(secrets, "chalkctl", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := osCAPool(t, secrets)
	conn, err := sdk.Dial(addr, sdk.Trust{OSCAs: pool, Name: "n1"}, cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Info(context.Background(), &nodev1.InfoRequest{}); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	conn, err = sdk.Dial(addr, sdk.Trust{OSCAs: pool, Name: "n2"}, cert)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Info(context.Background(), &nodev1.InfoRequest{}); err == nil {
		t.Fatal("verified n1 as n2")
	}
}

func TestDialMaintenanceMapsARefusedCertificate(t *testing.T) {
	addr := serveMaintenanceWithOSCA(t)
	other, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := sdk.AdminCertificate(other, "chalkctl", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conn, _, _, err := sdk.DialMaintenance(context.Background(), addr, sdk.Trust{Insecure: true}, cert)
	if conn != nil {
		defer conn.Close()
	}
	if !errors.Is(err, sdk.ErrOSCARefused) {
		t.Fatalf("err %v, want ErrOSCARefused", err)
	}
}

// TestDialMaintenanceWithoutACertificateIsNotOSCARefused checks that a handshake that fails for
// want of any client certificate, cert nil, is never mapped to ErrOSCARefused: nothing of cert's
// was refused.
func TestDialMaintenanceWithoutACertificateIsNotOSCARefused(t *testing.T) {
	addr := serveMaintenanceWithOSCA(t)
	conn, _, _, err := sdk.DialMaintenance(context.Background(), addr, sdk.Trust{Insecure: true}, nil)
	if conn != nil {
		defer conn.Close()
	}
	if err == nil {
		t.Fatal("dialled a node that requires a client certificate without presenting one")
	}
	if errors.Is(err, sdk.ErrOSCARefused) {
		t.Errorf("err %v, want it not mapped to ErrOSCARefused since no certificate was presented", err)
	}
}

// TestTrustRefusesNoMode checks that a Trust naming none of a fingerprint, OS CAs or insecure is
// refused.
func TestTrustRefusesNoMode(t *testing.T) {
	if _, err := sdk.Dial("127.0.0.1:0", sdk.Trust{}, nil); err == nil {
		t.Fatal("dialled with a trust that names no mode")
	}
}

// TestTrustRefusesTwoModes checks that a Trust naming more than one of a fingerprint, OS CAs and
// insecure is refused as ambiguous.
func TestTrustRefusesTwoModes(t *testing.T) {
	if _, err := sdk.Dial("127.0.0.1:0", sdk.Trust{Fingerprint: strings.Repeat("00", 32), Insecure: true}, nil); err == nil {
		t.Fatal("dialled with a trust that names two modes")
	}
}

// TestDialMaintenanceRefusesAnAmbiguousTrust checks DialMaintenance's own refusals of the trust
// it is given, before it dials anything: it names neither or both of a fingerprint and insecure,
// names an OS CA, or names Untrusted, which ErrOSCARefused already explains refusals there.
func TestDialMaintenanceRefusesAnAmbiguousTrust(t *testing.T) {
	pool := x509.NewCertPool()
	for _, tc := range []struct {
		name string
		t    sdk.Trust
	}{
		{"neither a fingerprint nor insecure", sdk.Trust{}},
		{"both a fingerprint and insecure", sdk.Trust{Fingerprint: strings.Repeat("00", 32), Insecure: true}},
		{"an OS CA", sdk.Trust{Insecure: true, OSCAs: pool}},
		{"Untrusted", sdk.Trust{Insecure: true, Untrusted: func(error, bool) error { return nil }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := sdk.DialMaintenance(context.Background(), "127.0.0.1:0", tc.t, nil); err == nil {
				t.Fatalf("dialled maintenance with a trust naming %s", tc.name)
			}
		})
	}
}

// serveTLS serves h with the configuration as chalkd does, and returns the address. httptest's
// servers would add a certificate of their own, which TLS prefers to GetCertificate.
func serveTLS(t *testing.T, h http.Handler, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, TLSConfig: cfg}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// stayUp is a reboot of a test node that does not start, so the node keeps serving.
func stayUp() error { return errors.New("the test node stays up") }

// maintenanceNode is a chalkd of maintenance mode that fails the test if it is asked to install.
func maintenanceNode() *chalkd.Server {
	return &chalkd.Server{
		Mode:       nodev1.Mode_MODE_MAINTENANCE,
		InPlace:    func(context.Context, install.Request, []byte) error { return errors.New("unexpected install in place") },
		FromParts:  func(context.Context, install.PartsRequest) error { return errors.New("unexpected install onto a disk") },
		RebootNode: stayUp,
	}
}

// serveMaintenance serves a maintenance-mode chalkd that accepts any client, as an image without
// an OS CA baked in does, and returns its address and its self-signed certificate's fingerprint.
func serveMaintenance(t *testing.T) (addr, fingerprint string) {
	t.Helper()
	s := maintenanceNode()
	s.AnyClient = true
	self, err := pki.SelfSigned("chalkd", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	s.Fingerprint = pki.Fingerprint(leaf.Raw)
	addr = serveTLS(t, s.Handler(), chalkd.TLSConfig(chalkd.StaticCertificate(&pair), nil))
	return addr, s.Fingerprint
}

// serveMaintenanceWithOSCA serves a maintenance-mode chalkd that requires client certificates of
// its own OS CA alone, as an image built from a secrets.pub.json does, and returns its address.
func serveMaintenanceWithOSCA(t *testing.T) string {
	t.Helper()
	s := maintenanceNode()
	osca, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	self, err := pki.SelfSigned("chalkd", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(self.Certificate), []byte(self.Key))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pki.BundlePool(osca.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	s.ClientCAs = chalkd.StaticCAs(pool)
	return serveTLS(t, s.Handler(), chalkd.TLSConfig(chalkd.StaticCertificate(&pair), s.ClientCAs))
}

// serveNormal serves a normal-mode chalkd with a node certificate for n1 and returns its address,
// that certificate's fingerprint and the secrets its OS CA comes from. It accepts clients without
// a certificate too, so a probe that presents none still reaches Info; what it checks is the
// node's own certificate, not who calls it.
func serveNormal(t *testing.T) (addr, fingerprint string, secrets clustersecrets.Secrets) {
	t.Helper()
	secrets, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nodeCert, err := pki.IssueNode(secrets.NodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, chalkd.NodeCertificateFile), nodeCert.Certificate+nodeCert.Key)
	writeFile(t, filepath.Join(dir, chalkd.CAFile), secrets.OSCA.Certificate)
	cert, err := chalkd.LoadNodeCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &chalkd.Server{Mode: nodev1.Mode_MODE_NORMAL, AnyClient: true, Certificate: cert}
	addr = serveTLS(t, s.Handler(), chalkd.TLSConfig(cert.GetCertificate, nil))
	return addr, cert.Fingerprint(), secrets
}

// osCAPool is the pool secrets' OS CA bundle verifies an installed node's certificate with.
func osCAPool(t *testing.T, secrets clustersecrets.Secrets) *x509.CertPool {
	t.Helper()
	pool, err := pki.BundlePool(secrets.OSCABundle())
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
