package sdk_test

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/sdk"
	"github.com/trevex/chalkos/pkg/sdk/sdktest"
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

// An installed node is reached by the secrets' OS CAs under its own name alone.
func TestDialInstalledChecksTheName(t *testing.T) {
	addr, _, secrets := serveNormal(t)
	cert, err := sdk.AdminCertificate(secrets, "sdk-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conn, info, err := sdk.DialInstalled(context.Background(), addr, secrets, "n1", cert)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if info.NodeName != "n1" || len(info.RootHash) == 0 {
		t.Errorf("info = %v, want n1's with its root hash", info)
	}
	conn, info, err = sdk.DialInstalled(context.Background(), addr, secrets, "n2", cert)
	if !errors.Is(err, sdk.ErrOtherNode) || conn != nil || info != nil {
		t.Fatalf("n1 dialled as n2: %v, %v", info, err)
	}
}

// A node in maintenance mode, whose certificate no OS CA issued, is not installed, as it answers
// unverified, and the error keeps the verification's failure.
func TestDialInstalledRefusesANodeInMaintenance(t *testing.T) {
	secrets, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := sdktest.Serve(t, sdktest.Maintenance(), secrets.OSCA, pki.CertKey{}, "", nil).Addr
	cert, err := sdk.AdminCertificate(secrets, "sdk-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conn, info, err := sdk.DialInstalled(context.Background(), addr, secrets, "n1", cert)
	if !errors.Is(err, sdk.ErrNotInstalled) || conn != nil || info != nil {
		t.Fatalf("dialled a node in maintenance mode as installed: %v, %v", info, err)
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); !ok {
		t.Errorf("err %v, want it to wrap the verification's failure", err)
	}
}

// A node whose certificate another cluster's OS CA issued, under the name dialled or another, is
// neither installed under another name nor in maintenance mode.
func TestDialInstalledRefusesAnotherCluster(t *testing.T) {
	addr, _, _ := serveNormal(t)
	other, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := sdk.AdminCertificate(other, "sdk-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"n1", "n2"} {
		_, _, err = sdk.DialInstalled(context.Background(), addr, other, name, cert)
		if err == nil || errors.Is(err, sdk.ErrNotInstalled) || errors.Is(err, sdk.ErrOtherNode) {
			t.Errorf("as %s: err %v, want the verification's failure", name, err)
		}
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

// serveMaintenance serves a maintenance-mode chalkd that accepts any client, as an image without
// an OS CA baked in does, and returns its address and its self-signed certificate's fingerprint.
func serveMaintenance(t *testing.T) (addr, fingerprint string) {
	t.Helper()
	s := sdktest.Maintenance()
	s.AnyClient = true
	node := sdktest.Serve(t, s, pki.CertKey{}, pki.CertKey{}, "", nil)
	return node.Addr, node.Fingerprint
}

// serveMaintenanceWithOSCA serves a maintenance-mode chalkd that requires client certificates of
// its own OS CA alone, as an image built from a secrets.pub.json does, and returns its address.
func serveMaintenanceWithOSCA(t *testing.T) string {
	t.Helper()
	osca, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sdktest.Serve(t, sdktest.Maintenance(), osca, pki.CertKey{}, "", nil).Addr
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
	s := &chalkd.Server{Mode: nodev1.Mode_MODE_NORMAL, AnyClient: true}
	node := sdktest.Serve(t, s, secrets.OSCA, secrets.NodeCA, "n1", nil)
	return node.Addr, node.Fingerprint, secrets
}
