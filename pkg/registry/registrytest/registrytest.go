// Package registrytest serves pkg/registry on loopback for tests, with a node certificate of an
// OS CA the test gives, as chalkd keeps it.
package registrytest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
)

// Registry is a registry served over TLS on a loopback address.
type Registry struct {
	*registry.Server
	// Host is the registry's address and port, as image references name it.
	Host string
	// Certificate and CA are the files the registry reads its node certificate and the OS CAs
	// from; NodeCA issued the node certificate.
	Certificate, CA string
	NodeCA          pki.CertKey

	roots  *x509.CertPool
	server *httptest.Server
}

// Start serves a registry until the test ends: on ::1 with ipv6, else on 127.0.0.1, with a node
// certificate for both addresses from a node CA of osCA. wrap, when not nil, wraps the
// registry's handler, as a test that breaks some requests does.
func Start(t testing.TB, osCA pki.CertKey, ipv6 bool, wrap func(http.Handler) http.Handler) *Registry {
	t.Helper()
	now := time.Now()
	nodeCA, err := pki.NewNodeCA(osCA, now)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r := &Registry{
		Certificate: filepath.Join(dir, "node.pem"),
		CA:          filepath.Join(dir, "ca.crt"),
		NodeCA:      nodeCA,
		roots:       x509.NewCertPool(),
	}
	r.roots.AppendCertsFromPEM([]byte(osCA.Certificate))
	r.WriteNodeCertificate(t)
	if err := atomicfile.WriteFile(r.CA, []byte(osCA.Certificate), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Server, err = registry.New(context.Background(), registry.Config{Storage: filepath.Join(dir, "storage"), Certificate: r.Certificate, CA: r.CA})
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler = r.Server
	if wrap != nil {
		h = wrap(h)
	}
	r.server = httptest.NewUnstartedServer(h)
	if ipv6 {
		l, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Fatal(err)
		}
		r.server.Listener.Close()
		r.server.Listener = l
	}
	r.server.TLS = r.TLSConfig()
	r.server.StartTLS()
	t.Cleanup(r.Close)
	r.Host = r.server.Listener.Addr().String()
	return r
}

// WriteNodeCertificate issues a new node certificate for 127.0.0.1 and ::1 and replaces the
// registry's with it, as chalkd renews it: the chain, then the key, renamed over the old file.
func (r *Registry) WriteNodeCertificate(t testing.TB) *x509.Certificate {
	t.Helper()
	names := pki.NodeNames{CommonName: "registry", IPs: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}}
	cert, err := pki.IssueNode(r.NodeCA, names, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(r.Certificate, []byte(cert.Certificate+cert.Key), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf, err := pki.FirstCertificate([]byte(cert.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// Transport reaches the registry trusting the OS CA, presenting client when it is not nil.
func (r *Registry) Transport(t testing.TB, client *pki.CertKey) *http.Transport {
	t.Helper()
	config := &tls.Config{RootCAs: r.roots}
	if client != nil {
		pair, err := tls.X509KeyPair([]byte(client.Certificate), []byte(client.Key))
		if err != nil {
			t.Fatal(err)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	return &http.Transport{TLSClientConfig: config}
}

// Close stops serving.
func (r *Registry) Close() { r.server.Close() }
