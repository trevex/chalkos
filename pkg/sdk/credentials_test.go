package sdk_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/sdk"
)

// TestAdminCertificateIsAnHourOfAdmin checks that AdminCertificate issues a certificate for the
// name given that grants the admin role and expires after AdminValidity.
func TestAdminCertificateIsAnHourOfAdmin(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, err := clustersecrets.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := sdk.AdminCertificate(s, "chalkctl", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "chalkctl" {
		t.Errorf("name %q, want %q", leaf.Subject.CommonName, "chalkctl")
	}
	if got := leaf.NotAfter.Sub(now); got > sdk.AdminValidity || got < sdk.AdminValidity-time.Minute {
		t.Errorf("valid for %s, want %s", got, sdk.AdminValidity)
	}
	if role, ok := pki.RoleOf(leaf); !ok || role != pki.RoleAdmin {
		t.Errorf("role %q (ok=%v), want %q", role, ok, pki.RoleAdmin)
	}
}
