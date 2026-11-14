// Package client connects to chalkd. It verifies a node in maintenance mode by its certificate's
// fingerprint and an installed node by the OS CA, and presents a client certificate when it has
// one.
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/pki"
)

// Port is where chalkd listens.
const Port = "50000"

// Options say how to verify the node and how to authenticate to it.
type Options struct {
	// Verify checks the node's certificate. Whichever it is, Conn.Fingerprint reports the
	// certificate the node presented.
	Verify Verifier
	// Certificate is presented to the node; nil presents none. GetClientCertificate, when set,
	// replaces it.
	Certificate          *tls.Certificate
	GetClientCertificate func(*tls.CertificateRequestInfo) (*tls.Certificate, error)
	// Source says where the address came from, such as --address; an error reaching it names it.
	Source string
	// Untrusted, when set, replaces the error of a call whose TLS handshake failed for want of
	// trust in either direction; refused tells whether the node refused Certificate.
	Untrusted func(err error, refused bool) error
}

// Conn is a client of one node.
type Conn struct {
	nodev1connect.NodeServiceClient
	transport   *http.Transport
	mu          sync.Mutex
	fingerprint string
	// conns are the open connections, which Close ends.
	conns map[*trackedConn]struct{}
	// closed is set by Close: a dial still under way then gets a connection nothing would end.
	closed bool
	// netDial connects to the node; tests replace it.
	netDial func(ctx context.Context, network, addr string) (net.Conn, error)
	// source says where the address came from.
	source string
	// http and baseURL reach the node's services.
	http    *http.Client
	baseURL string
}

// errClosed is what calls after Close fail with.
var errClosed = errors.New("the client of the node is closed")

// Close ends every connection to the node and fails later calls. It also ends a connection that a
// timed-out call left waiting on HTTP/2's ping, and one whose dial completes later.
func (c *Conn) Close() {
	c.mu.Lock()
	conns := c.conns
	c.conns, c.closed = nil, true
	c.mu.Unlock()
	c.transport.CloseIdleConnections()
	for conn := range conns {
		conn.Close()
	}
}

// dial connects to the node and keeps the connection for Close.
func (c *Conn) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errClosed
	}
	conn, err := c.netDial(ctx, network, addr)
	if err != nil {
		if c.source != "" {
			return nil, fmt.Errorf("reach %s (from %s): %w", addr, c.source, err)
		}
		return nil, err
	}
	t := &trackedConn{Conn: conn, owner: c}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		conn.Close()
		return nil, errClosed
	}
	if c.conns == nil {
		c.conns = map[*trackedConn]struct{}{}
	}
	c.conns[t] = struct{}{}
	return t, nil
}

// trackedConn is a connection of a Conn, which forgets it once it is closed.
type trackedConn struct {
	net.Conn
	owner *Conn
}

func (t *trackedConn) Close() error {
	t.owner.mu.Lock()
	delete(t.owner.conns, t)
	t.owner.mu.Unlock()
	return t.Conn.Close()
}

// Fingerprint returns the fingerprint of the certificate the node presented, once connected.
func (c *Conn) Fingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fingerprint
}

// Dial prepares a client of the node at endpoint, host or host:port; the port defaults to
// chalkd's. The TLS handshake happens with the first call, before any request is sent.
func Dial(endpoint string, o Options) (*Conn, error) {
	if o.Verify == nil {
		return nil, errors.New("no way to verify the node is given")
	}
	if err := o.Verify.check(); err != nil {
		return nil, err
	}
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		endpoint = net.JoinHostPort(endpoint, Port)
	}
	c := &Conn{netDial: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, source: o.Source}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Verification is VerifyConnection's job: by fingerprint, or by the CA for the node's
		// name, which may differ from the address it is reached at.
		InsecureSkipVerify: true,
		VerifyConnection:   func(cs tls.ConnectionState) error { return c.verifyConnection(cs, o.Verify) },
	}
	if o.Certificate != nil {
		cfg.Certificates = []tls.Certificate{*o.Certificate}
	}
	cfg.GetClientCertificate = o.GetClientCertificate
	transport := &http.Transport{
		DialContext:         c.dial,
		TLSClientConfig:     cfg,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	c.transport = transport
	var rt http.RoundTripper = transport
	if o.Untrusted != nil {
		rt = &untrusted{RoundTripper: transport, conn: c, endpoint: endpoint, tls: cfg, replace: o.Untrusted}
	}
	c.http = &http.Client{Transport: rt}
	c.baseURL = "https://" + endpoint
	c.NodeServiceClient = nodev1connect.NewNodeServiceClient(c.http, c.baseURL)
	return c, nil
}

// DialSocket prepares a client of chalkd's console socket at path, which serves the node API
// without TLS to root alone. Calls dial the socket again after chalkd restarts.
func DialSocket(path string) *Conn {
	c := &Conn{}
	c.netDial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			return nil, fmt.Errorf("reach chalkd at %s: %w", path, err)
		}
		return conn, nil
	}
	c.transport = &http.Transport{DialContext: c.dial}
	c.http = &http.Client{Transport: c.transport}
	c.baseURL = "http://chalkd"
	c.NodeServiceClient = nodev1connect.NewNodeServiceClient(c.http, c.baseURL)
	return c
}

// HTTPClient and BaseURL reach the node's other services, such as a layer's, over the
// connections and checks of this client: connect's New…Client takes them.
func (c *Conn) HTTPClient() connect.HTTPClient { return c.http }
func (c *Conn) BaseURL() string                { return c.baseURL }

// verifyConnection records the fingerprint of the node's certificate and checks the certificate
// with v.
func (c *Conn) verifyConnection(cs tls.ConnectionState, v Verifier) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("the node presented no certificate")
	}
	fp := pki.Fingerprint(cs.PeerCertificates[0].Raw)
	c.mu.Lock()
	c.fingerprint = fp
	c.mu.Unlock()
	return v.verify(cs.PeerCertificates, fp)
}

// untrusted replaces the error of a request whose TLS handshake failed for want of trust. Every
// call, unary or streaming, goes through the transport, so wrapping it covers all of them.
type untrusted struct {
	http.RoundTripper
	conn     *Conn
	endpoint string
	tls      *tls.Config
	replace  func(error, bool) error
}

func (u *untrusted) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := u.RoundTripper.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	if bad, refused := distrusted(err); bad {
		return nil, u.replace(err, refused)
	}
	// In TLS 1.3 the client's handshake ends before the node checks the client's certificate, so
	// the node's alert may reach the request only as a broken connection; a fresh connection
	// reads the alert itself.
	if req.Context().Err() != nil || !alertShaped(err) {
		return nil, err
	}
	if failed := readAlert(req.Context(), u.conn.netDial, u.endpoint, u.tls); failed != nil {
		_, refused := distrusted(failed)
		return nil, u.replace(failed, refused)
	}
	return nil, err
}

// alertShaped reports whether err may hide the node's TLS alert. When the alert closes the
// connection before the first stream, http2 returns an unexported sentinel that wraps nothing,
// so only its text identifies it.
func alertShaped(err error) bool {
	if op, ok := errors.AsType[*net.OpError](err); ok && op.Op == "remote error" {
		return true
	}
	if strings.Contains(err.Error(), "http2: client conn could not be established") {
		return true
	}
	for _, closed := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE} {
		if errors.Is(err, closed) {
			return true
		}
	}
	return false
}

// readAlert connects to addr with cfg and reads the node's first bytes without sending any. It
// returns the error when the node and the client distrust each other, and nil otherwise.
func readAlert(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), addr string, cfg *tls.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil
	}
	defer raw.Close()
	cfg = cfg.Clone()
	cfg.NextProtos = []string{"h2"}
	conn := tls.Client(raw, cfg)
	if err = conn.HandshakeContext(ctx); err == nil {
		deadline, _ := ctx.Deadline()
		conn.SetReadDeadline(deadline)
		_, err = conn.Read(make([]byte, 1))
	}
	if err != nil {
		if bad, _ := distrusted(err); bad {
			return err
		}
	}
	return nil
}

// RefusesCertificate reports whether the node at addr refused cert for an unknown issuer, given
// err from a call to it; it reconnects unverified when err may hide the alert. Only the unknown
// certificate authority alert counts: the node sends a bad certificate alert for other
// reasons too, such as an expired certificate.
func RefusesCertificate(ctx context.Context, err error, addr string, cert *tls.Certificate) bool {
	if cert == nil || err == nil {
		return false
	}
	if unknownAuthority(err) {
		return true
	}
	if ctx.Err() != nil || !alertShaped(err) {
		return false
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, Port)
	}
	cfg := &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS13}
	return unknownAuthority(readAlert(ctx, (&net.Dialer{}).DialContext, addr, cfg))
}

// unknownAuthority reports whether err is the node's alert that it does not know the issuer of
// the client's certificate.
func unknownAuthority(err error) bool {
	return err != nil && strings.Contains(err.Error(), "remote error: tls: unknown certificate authority")
}

// distrusted reports whether err is a TLS handshake's failure for want of trust, and whether the
// node refused the client's certificate. A remote alert arrives as a net.OpError of an unexported
// type, so only its text tells which alert it was.
func distrusted(err error) (bad, refused bool) {
	if err == nil {
		return false, false
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true, false
	}
	msg := err.Error()
	refused = strings.Contains(msg, "remote error: tls: unknown certificate authority") || strings.Contains(msg, "remote error: tls: bad certificate")
	return refused, refused
}
