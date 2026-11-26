// Package sdktest serves in-process chalkd nodes for the tests of chalkctl, the SDK and its
// clients.
package sdktest

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
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	imagecontract "github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema/schematest"
)

// Node is a chalkd served over TLS.
type Node struct {
	// Addr is the node's address.
	Addr string
	// Fingerprint is that of the certificate the node first served.
	Fingerprint string
	// Served holds the certificate the node presents; storing another replaces it, as a reboot
	// in maintenance mode does.
	Served *atomic.Pointer[tls.Certificate]
}

// StayUp is a reboot or shutdown of a test node that does not start, so the node keeps serving
// and its change locks stay free.
func StayUp() error { return errors.New("the test node stays up") }

// Maintenance is a chalkd in maintenance mode that refuses installs unless the test sets InPlace
// or FromParts.
func Maintenance() *chalkd.Server {
	return &chalkd.Server{
		Mode:       nodev1.Mode_MODE_MAINTENANCE,
		InPlace:    func(context.Context, install.Request, []byte) error { return errors.New("unexpected install in place") },
		FromParts:  func(context.Context, install.PartsRequest) error { return errors.New("unexpected install onto a disk") },
		RebootNode: StayUp,
	}
}

// ServeTLS serves h with cfg as chalkd does and returns the address. httptest's servers would
// add a certificate of their own, which TLS prefers to GetCertificate.
func ServeTLS(t testing.TB, h http.Handler, cfg *tls.Config) string {
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

// Serve serves s with the OS CA osCA: in normal mode with a node certificate of nodeCA for name,
// in maintenance mode with a self-signed one. It sets s's ClientCAs from osCA, unless s already
// sets them or accepts any client, as chalkd does only in maintenance mode on an image without
// an OS CA. Serve sets s's Fingerprint and, where s leaves them unset, a node certificate, an
// os-release naming the image test-metal on metal, a cmdline booting the root hash abab… and a
// contract of Schemas. nodeCA and name matter in normal mode only. wrap, when not nil, wraps the
// handler, as tests that record or delay requests do.
func Serve(t testing.TB, s *chalkd.Server, osCA, nodeCA pki.CertKey, name string, wrap func(http.Handler) http.Handler) Node {
	t.Helper()
	var cert pki.CertKey
	var err error
	if s.Mode == nodev1.Mode_MODE_NORMAL {
		cert, err = pki.IssueNode(nodeCA, pki.NodeNames{CommonName: name, DNSNames: []string{name}}, time.Now())
	} else {
		cert, err = pki.SelfSigned("chalkd", time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode == nodev1.Mode_MODE_NORMAL && s.Certificate == nil {
		dir := filepath.Join(t.TempDir(), "chalkd")
		writeFile(t, filepath.Join(dir, chalkd.NodeCertificateFile), cert.Certificate+cert.Key)
		writeFile(t, filepath.Join(dir, chalkd.CAFile), osCA.Certificate)
		if s.Certificate, err = chalkd.LoadNodeCertificate(dir); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.Key))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	s.Fingerprint = pki.Fingerprint(leaf.Raw)
	if s.Paths.OSRelease == "" {
		s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
		writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_IMAGE=test-metal\nCHALKOS_PLATFORM=metal\n")
	}
	ContractDir(t, s)
	// It boots the store of the root hash abab…, whose config slot an install in place writes.
	if s.Paths.Cmdline == "" {
		s.Paths.Cmdline = filepath.Join(t.TempDir(), "cmdline")
		writeFile(t, s.Paths.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	}
	if !s.AnyClient && s.ClientCAs == nil {
		ca, err := pki.ParseCertificate([]byte(osCA.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AddCert(ca)
		s.ClientCAs = chalkd.StaticCAs(pool)
	}
	h := s.Handler()
	if wrap != nil {
		h = wrap(h)
	}
	served := &atomic.Pointer[tls.Certificate]{}
	served.Store(&pair)
	addr := ServeTLS(t, h, chalkd.TLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return served.Load(), nil }, s.ClientCAs))
	return Node{Addr: addr, Fingerprint: s.Fingerprint, Served: served}
}

// Schemas are the schemas a server without a contract of its own gets.
var Schemas = []string{schematest.Builtin, schematest.Networkd}

// ContractDir is the directory of s's contract; a server without one gets Schemas.
func ContractDir(t testing.TB, s *chalkd.Server) string {
	t.Helper()
	if s.Loader.Contract == nil {
		dir := t.TempDir()
		schematest.Copy(t, filepath.Join(dir, imagecontract.SchemasDir), Schemas...)
		LoadContract(t, s, dir)
	}
	return s.Loader.Contract.Dir
}

// WriteContract writes the file name of s's contract and loads the contract again, as chalkd
// loads the running image's as it starts.
func WriteContract(t testing.TB, s *chalkd.Server, name, content string) {
	t.Helper()
	dir := ContractDir(t, s)
	writeFile(t, filepath.Join(dir, name), content)
	LoadContract(t, s, dir)
}

// LoadContract makes the contract of dir s's.
func LoadContract(t testing.TB, s *chalkd.Server, dir string) {
	t.Helper()
	var opts []imagecontract.Option
	if s.Loader.Render.Root != "" {
		opts = append(opts, imagecontract.RenderRoot(s.Loader.Render.Root))
	}
	c, err := imagecontract.Load(dir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	s.Loader.Contract = c
}

// writeFile writes content to the file at path, creating its directory.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
