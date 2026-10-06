package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/pki"
)

type infoHandler struct {
	nodev1connect.UnimplementedNodeServiceHandler
	calls  atomic.Int32
	client atomic.Value
}

func (h *infoHandler) Info(ctx context.Context, _ *connect.Request[nodev1.InfoRequest]) (*connect.Response[nodev1.InfoResponse], error) {
	h.calls.Add(1)
	return connect.NewResponse(&nodev1.InfoResponse{Hostname: "n1"}), nil
}

// serve runs a TLS server with the node certificate that records the client's certificate.
func serve(t *testing.T, cert pki.CertKey) (*infoHandler, string) {
	t.Helper()
	h := &infoHandler{}
	_, handler := nodev1connect.NewNodeServiceHandler(h)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) > 0 {
			h.client.Store(r.TLS.PeerCertificates[0].Subject.CommonName)
		}
		handler.ServeHTTP(w, r)
	}))
	pair, err := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.Key))
	if err != nil {
		t.Fatal(err)
	}
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return h, srv.Listener.Addr().String()
}

func info(c *Conn) error {
	_, err := c.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
	return err
}

func TestPinnedFingerprint(t *testing.T) {
	self, err := pki.SelfSigned("chalkd", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h, addr := serve(t, self)
	cert, _ := pki.ParseCertificate([]byte(self.Certificate))
	fp := pki.Fingerprint(cert.Raw)

	c, err := Dial(addr, Options{Fingerprint: strings.ToUpper(fp)})
	if err != nil {
		t.Fatal(err)
	}
	if err := info(c); err != nil {
		t.Fatalf("pinned fingerprint refused: %v", err)
	}

	other, err := Dial(addr, Options{Fingerprint: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	before := h.calls.Load()
	if err := info(other); err == nil || !strings.Contains(err.Error(), "fingerprint "+fp) {
		t.Fatalf("err = %v, want a fingerprint mismatch naming %s", err, fp)
	}
	if h.calls.Load() != before {
		t.Error("a request reached a node with the wrong fingerprint")
	}
}

func TestInsecureReportsFingerprint(t *testing.T) {
	self, _ := pki.SelfSigned("chalkd", time.Now())
	_, addr := serve(t, self)
	c, err := Dial(addr, Options{Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := info(c); err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.ParseCertificate([]byte(self.Certificate))
	if c.Fingerprint() != pki.Fingerprint(cert.Raw) {
		t.Errorf("fingerprint = %q", c.Fingerprint())
	}
}

func TestCAVerifiesNodeName(t *testing.T) {
	now := time.Now()
	ca, _ := pki.NewCA("chalkos OS CA", now)
	node, _ := pki.IssueNode(ca, "n1", []string{"n1"}, nil, now)
	admin, _ := pki.IssueClient(ca, "admin", pki.RoleAdmin, now)
	h, addr := serve(t, node)
	caCert, _ := pki.ParseCertificate([]byte(ca.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	pair, _ := tls.X509KeyPair([]byte(admin.Certificate), []byte(admin.Key))

	c, err := Dial(addr, Options{CA: pool, ServerName: "n1", Certificate: &pair})
	if err != nil {
		t.Fatal(err)
	}
	if err := info(c); err != nil {
		t.Fatalf("node certificate refused: %v", err)
	}
	if got := h.client.Load(); got != "admin" {
		t.Errorf("client certificate seen by the node = %v", got)
	}

	wrong, _ := Dial(addr, Options{CA: pool, ServerName: "n2"})
	if err := info(wrong); err == nil {
		t.Error("accepted a node certificate issued for another node")
	}
	otherCA, _ := pki.NewCA("other", now)
	otherCert, _ := pki.ParseCertificate([]byte(otherCA.Certificate))
	otherPool := x509.NewCertPool()
	otherPool.AddCert(otherCert)
	foreign, _ := Dial(addr, Options{CA: otherPool, ServerName: "n1"})
	if err := info(foreign); err == nil {
		t.Error("accepted a node certificate from another CA")
	}
}

func TestDialOptions(t *testing.T) {
	for _, o := range []Options{{}, {Insecure: true, Fingerprint: "ab"}} {
		if _, err := Dial("127.0.0.1", o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}

// Without a server name, the CA would accept the certificate of any node it issued.
func TestCARequiresServerName(t *testing.T) {
	ca, _ := pki.NewCA("chalkos OS CA", time.Now())
	caCert, _ := pki.ParseCertificate([]byte(ca.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if c, err := Dial("127.0.0.1", Options{CA: pool}); err == nil || c != nil {
		t.Fatalf("Dial = %v, %v; want a refusal before connecting", c, err)
	}
}
