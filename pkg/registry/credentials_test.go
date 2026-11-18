package registry_test

import (
	"crypto/x509"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
)

// TestWritesVerifiedPerRequest refuses a write of an admin whose OS CA the registry stopped
// trusting, on the connection whose handshake verified it, and closes that connection.
func TestWritesVerifiedPerRequest(t *testing.T) {
	r := startRegistry(t)
	client := r.clients["admin"]
	collect := func() (status int, reused bool, closed bool) {
		t.Helper()
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodPost, "https://"+r.Host+registry.GCPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, reused, resp.Close
	}
	if status, _, _ := collect(); status != http.StatusOK {
		t.Fatalf("the admin's collection: %d", status)
	}
	otherCA, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(otherCA.Certificate))
	r.SetRoots(roots)
	status, reused, closed := collect()
	if status != http.StatusUnauthorized || !reused || !closed {
		t.Errorf("a write once the OS CA is gone: %d, reused %v, closed %v; want 401 on the same connection, which closes", status, reused, closed)
	}
}

// TestNewNeedsCredentials refuses to start without a node certificate, and names the file.
func TestNewNeedsCredentials(t *testing.T) {
	dir := t.TempDir()
	c := registry.Config{Storage: filepath.Join(dir, "storage"), Certificate: filepath.Join(dir, "node.pem"), CA: filepath.Join(dir, "ca.crt")}
	if _, err := registry.New(t.Context(), c); err == nil || !strings.Contains(err.Error(), "the node certificate") {
		t.Errorf("New without files: %v", err)
	}
	if err := os.WriteFile(c.Certificate, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.New(t.Context(), c); err == nil || !strings.Contains(err.Error(), "the node certificate") {
		t.Errorf("New with a broken certificate: %v", err)
	}
}
