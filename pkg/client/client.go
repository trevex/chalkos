// Package client connects to chalkd. It verifies a node in maintenance mode by its certificate's
// fingerprint and an installed node by the OS CA, and presents a client certificate when it has
// one.
package client

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
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
	// Certificate is presented to the node; nil presents none. GetClientCertificate, when set,
	// replaces it.
	Certificate          *tls.Certificate
	GetClientCertificate func(*tls.CertificateRequestInfo) (*tls.Certificate, error)
}

// Conn is a client of one node.
type Conn struct {
	nodev1connect.NodeServiceClient
	mu          sync.Mutex
	fingerprint string
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
	if o.AnyNode && (o.CA == nil || o.ServerName != "") {
		return nil, errors.New("accepting any node needs the CA and no node's name")
	}
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		endpoint = net.JoinHostPort(endpoint, Port)
	}
	c := &Conn{}
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
			chains, err := leaf.Verify(x509.VerifyOptions{Roots: o.CA, Intermediates: intermediates, DNSName: o.ServerName,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
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
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:     cfg,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	c.NodeServiceClient = nodev1connect.NewNodeServiceClient(&http.Client{Transport: transport}, "https://"+endpoint)
	return c, nil
}
