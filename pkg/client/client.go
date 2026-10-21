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

	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/pki"
)

// Port is where chalkd listens.
const Port = "50000"

// Options say how to verify the node and how to authenticate to it.
type Options struct {
	// Fingerprint pins the node's certificate by its SHA-256.
	Fingerprint string
	// Insecure accepts any certificate; Conn.Fingerprint reports what the node presented.
	Insecure bool
	// CA verifies the certificate of an installed node, which must be valid for ServerName.
	CA         *x509.CertPool
	ServerName string
	// AnyNode accepts, instead of ServerName, any node certificate that verifies against CA
	// through a node CA, as a node reaching a control plane through an address that names no
	// node, such as a VIP, does.
	AnyNode bool
	// IgnoreValidity verifies the node's chain by CA as of the node certificate's start, so a node
	// whose certificate expired is reached to deliver it a new one. Nothing else may use it.
	IgnoreValidity bool
	// Certificate is presented to the node; nil presents none. GetClientCertificate, when set,
	// replaces it.
	Certificate          *tls.Certificate
	GetClientCertificate func(*tls.CertificateRequestInfo) (*tls.Certificate, error)
	// Source says where the address came from, such as --address; an error reaching it names it.
	Source string
	// Untrusted, when set, gives the error of a call whose TLS handshake failed because the
	// node's certificate does not verify against CA or because the node refused Certificate for
	// an issuer it does not know; it is passed the handshake's error and whether the node
	// refused.
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
}

// errClosed is what calls after Close fail with.
var errClosed = errors.New("the client of the node is closed")

// Close ends every connection to the node, calls on them fail. A call that ended at its deadline
// may leave its connection open, waiting for an answer to HTTP/2's check of the connection, as
// long as the node keeps it open; Close ends that one too, and one whose dial completes later.
// Later calls fail.
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
	modes := 0
	for _, set := range []bool{o.Fingerprint != "", o.Insecure, o.CA != nil} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return nil, errors.New("exactly one of a fingerprint, insecure or a CA must be given")
	}
	// The CA issues every node's certificate; only the name tells this node from the others.
	if o.CA != nil && o.ServerName == "" && !o.AnyNode {
		return nil, errors.New("verifying the node by the CA needs the node's name")
	}
	if o.IgnoreValidity && (o.CA == nil || o.AnyNode) {
		return nil, errors.New("ignoring the validity needs the CA and the node's name")
	}
	if o.AnyNode && (o.CA == nil || o.ServerName != "") {
		return nil, errors.New("accepting any node needs the CA and no node's name")
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
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the node presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			fp := pki.Fingerprint(leaf.Raw)
			c.mu.Lock()
			c.fingerprint = fp
			c.mu.Unlock()
			switch {
			case o.Insecure:
				return nil
			case o.Fingerprint != "":
				if want := pki.NormalizeFingerprint(o.Fingerprint); fp != want {
					return fmt.Errorf("the node's certificate has the fingerprint %s, not %s", fp, want)
				}
				return nil
			}
			intermediates := x509.NewCertPool()
			for _, cert := range cs.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			verify := x509.VerifyOptions{Roots: o.CA, Intermediates: intermediates, DNSName: o.ServerName,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			if o.IgnoreValidity {
				// Each CA's validity covers the start of what it issued.
				verify.CurrentTime = leaf.NotBefore
			}
			chains, err := leaf.Verify(verify)
			if err != nil {
				return err
			}
			if role, _ := pki.ClientRole(chains); o.AnyNode && role != pki.RoleNode {
				return errors.New("the peer's certificate is not a node certificate")
			}
			return nil
		},
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
	c.NodeServiceClient = nodev1connect.NewNodeServiceClient(&http.Client{Transport: rt}, "https://"+endpoint)
	return c, nil
}

// untrusted replaces the error of a request whose TLS handshake failed for want of trust, as
// Options.Untrusted says. Every call, unary or streaming, sends its request through the
// transport, which performs the handshake and returns its error, so wrapping the transport covers
// all of them.
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
	// the node's alert may reach the request only as a broken connection. A connection of its
	// own, which reads the node's first bytes, receives the alert itself. Any other failure, and
	// a request its caller ended, has nothing to learn from one.
	if req.Context().Err() != nil || !alertShaped(err) {
		return nil, err
	}
	if failed := readAlert(req.Context(), u.conn.netDial, u.endpoint, u.tls); failed != nil {
		_, refused := distrusted(failed)
		return nil, u.replace(failed, refused)
	}
	return nil, err
}

// alertShaped reports whether err may hide the node's TLS alert: an alert of any kind, or a
// connection the node closed or reset.
func alertShaped(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "remote error" {
		return true
	}
	for _, closed := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE} {
		if errors.Is(err, closed) {
			return true
		}
	}
	return false
}

// readAlert connects to the node at addr with the TLS configuration cfg and reads its first
// bytes, the HTTP/2 settings of a node that accepts the client; it returns the error when the
// node and the client distrust each other, and nil otherwise, also when the node cannot be
// reached. Nothing is sent over the connection.
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

// RefusesCertificate reports whether the node at addr, host or host:port, refused cert for an
// issuer it does not know, given err, the error of a call to it. It connects again, without
// verifying the node, only when err may hide the node's alert.
func RefusesCertificate(ctx context.Context, err error, addr string, cert *tls.Certificate) bool {
	if cert == nil || err == nil {
		return false
	}
	if _, refused := distrusted(err); refused {
		return true
	}
	if ctx.Err() != nil || !alertShaped(err) {
		return false
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, Port)
	}
	cfg := &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS13}
	_, refused := distrusted(readAlert(ctx, (&net.Dialer{}).DialContext, addr, cfg))
	return refused
}

// distrusted reports whether err is a TLS handshake's failure for want of trust, and whether the
// node refused: an OS CA that does not verify the node's certificate fails on the client's side,
// and the node's alert that it refused the client's certificate on the node's. A remote alert
// reaches the client as a net.OpError of an unexported type, so only its text tells which alert
// it was.
func distrusted(err error) (bad, refused bool) {
	if err == nil {
		return false, false
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return true, false
	}
	msg := err.Error()
	refused = strings.Contains(msg, "remote error: tls: unknown certificate authority") || strings.Contains(msg, "remote error: tls: bad certificate")
	return refused, refused
}
