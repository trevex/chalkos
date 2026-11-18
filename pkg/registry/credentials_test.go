package registry_test

import (
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
)

// TestCredentialsReload serves the node certificate chalkd renamed over the old one from the next
// handshake on, verifies clients of an OS CA once the bundle holds it, and keeps the last
// certificate while the file is broken.
func TestCredentialsReload(t *testing.T) {
	r := startRegistry(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(r.osCA.Certificate))
	served := func() *big.Int {
		t.Helper()
		conn, err := tls.Dial("tcp", r.Host, &tls.Config{RootCAs: roots})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber
	}
	first := served()
	renewed := r.WriteNodeCertificate(t)
	if got := served(); got.Cmp(renewed.SerialNumber) != 0 || got.Cmp(first) == 0 {
		t.Errorf("after the renewal the registry serves %s, want %s", got, renewed.SerialNumber)
	}

	now := time.Now()
	otherCA, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.IssueClient(otherCA, "other", pki.RoleAdmin, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	client := r.Transport(t, &other)
	collect := func() (int, error) {
		req, err := http.NewRequest(http.MethodPost, "https://"+r.Host+registry.GCPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.RoundTrip(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := collect(); err == nil {
		t.Errorf("a client of an OS CA the node does not trust: %d", code)
	}
	if err := atomicfile.WriteFile(r.CA, []byte(pki.Bundle(r.osCA.Certificate, otherCA.Certificate)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, err := collect(); err != nil || code != http.StatusOK {
		t.Errorf("a client of an OS CA the node trusts now: %d, %v", code, err)
	}

	if err := atomicfile.WriteFile(r.Certificate, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := served(); got.Cmp(renewed.SerialNumber) != 0 {
		t.Errorf("with a broken file the registry serves %s, want the last one %s", got, renewed.SerialNumber)
	}
}

// TestNewNeedsCredentials refuses to start without the node certificate.
func TestNewNeedsCredentials(t *testing.T) {
	dir := t.TempDir()
	if _, err := registry.New(t.Context(), registry.Config{Storage: dir + "/storage", Certificate: dir + "/node.pem", CA: dir + "/ca.crt"}); err == nil {
		t.Error("New without files")
	}
}
