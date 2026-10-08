package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
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
	ca, _ := pki.NewOSCA(now)
	nodeCA, _ := pki.NewNodeCA(ca, now)
	node, _ := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, now)
	admin, _ := pki.IssueClient(ca, "admin", pki.RoleAdmin, time.Hour, now)
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
	otherCA, _ := pki.NewOSCA(now)
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

func TestAnyNode(t *testing.T) {
	now := time.Now()
	ca, _ := pki.NewOSCA(now)
	nodeCA, _ := pki.NewNodeCA(ca, now)
	node, _ := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "cp1"}, now)
	caCert, _ := pki.ParseCertificate([]byte(ca.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	_, addr := serve(t, node)
	c, err := Dial(addr, Options{CA: pool, AnyNode: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := info(c); err != nil {
		t.Errorf("a node certificate was refused: %v", err)
	}

	// A server certificate the OS CA issued itself is no node's.
	leaf, _ := pki.IssueLeaf(ca, pki.Leaf{CommonName: "cp1", Server: true}, now)
	_, rootAddr := serve(t, leaf)
	c, err = Dial(rootAddr, Options{CA: pool, AnyNode: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := info(c); err == nil || !strings.Contains(err.Error(), "not a node certificate") {
		t.Errorf("err = %v, want a refusal of a certificate that is not a node's", err)
	}
	for _, o := range []Options{{AnyNode: true}, {AnyNode: true, CA: pool, ServerName: "cp1"}} {
		if _, err := Dial(addr, o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}

func TestIgnoreValidityAcceptsAnExpiredNode(t *testing.T) {
	past := time.Now().Add(-2 * pki.LeafValidity)
	ca, _ := pki.NewOSCA(past)
	nodeCA, _ := pki.NewNodeCA(ca, past)
	expired, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, past)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := pki.ParseCertificate([]byte(ca.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	other, _ := pki.NewOSCA(time.Now())
	otherCert, _ := pki.ParseCertificate([]byte(other.Certificate))
	otherPool := x509.NewCertPool()
	otherPool.AddCert(otherCert)
	_, addr := serve(t, expired)
	for _, tc := range []struct {
		name string
		o    Options
		ok   bool
	}{
		{"with dates", Options{CA: pool, ServerName: "n1"}, false},
		{"without dates", Options{CA: pool, ServerName: "n1", IgnoreValidity: true}, true},
		{"without dates for another name", Options{CA: pool, ServerName: "n2", IgnoreValidity: true}, false},
		{"without dates by another CA", Options{CA: otherPool, ServerName: "n1", IgnoreValidity: true}, false},
	} {
		c, err := Dial(addr, tc.o)
		if err != nil {
			t.Fatal(err)
		}
		if err := info(c); (err == nil) != tc.ok {
			t.Errorf("%s: %v, want ok %v", tc.name, err, tc.ok)
		}
	}
	if _, err := Dial(addr, Options{Insecure: true, IgnoreValidity: true}); err == nil {
		t.Error("ignored the validity without a CA")
	}
}

// A connection whose dial completes after Close is closed at once: nothing outlives Close.
func TestCloseEndsALateConnection(t *testing.T) {
	self, _ := pki.SelfSigned("chalkd", time.Now())
	h, addr := serve(t, self)
	c, err := Dial(addr, Options{Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	dialed := make(chan net.Conn, 1)
	c.netDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		close(entered)
		<-release
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err == nil {
			dialed <- conn
		}
		return conn, err
	}
	result := make(chan error, 1)
	go func() { result <- info(c) }()
	<-entered
	c.Close()
	close(release)
	select {
	case err := <-result:
		if err == nil {
			t.Error("a call whose connection was dialled after Close succeeded")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the call did not end")
	}
	if h.calls.Load() != 0 {
		t.Error("a request reached the node after Close")
	}
	conn := <-dialed
	if _, err := conn.Write([]byte{0}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the connection dialled after Close is still open: %v", err)
	}
}
