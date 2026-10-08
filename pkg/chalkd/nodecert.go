package chalkd

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
)

// NodeCertificate is the certificate an installed node's chalkd serves and presents to renew it.
// It is replaced while chalkd runs: connections made afterwards get the new one.
type NodeCertificate struct {
	// path holds the chain and the key in one file, so a replacement switches both at once.
	path string
	// osCA is the PEM certificate of the OS CA the chain must lead to.
	osCA string

	// mu serialises replacements; current is read without it.
	mu      sync.Mutex
	current atomic.Pointer[pki.NodeCredential]
}

// NodeCertificateFile is the file below STATE's chalkd directory that holds the node's chain and
// key; CAFile holds the OS CA's certificate.
const (
	NodeCertificateFile = "node.pem"
	CAFile              = "ca.crt"
)

// LoadNodeCertificate reads the node certificate an installed node keeps in dir, STATE's chalkd
// directory. Its dates are not checked: a node whose certificate expired still serves it, so
// chalkctl node renew can reach it.
func LoadNodeCertificate(dir string) (*NodeCertificate, error) {
	osCA, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return nil, fmt.Errorf("read the OS CA: %w", err)
	}
	n := &NodeCertificate{path: filepath.Join(dir, NodeCertificateFile), osCA: string(osCA)}
	data, err := os.ReadFile(n.path)
	if err != nil {
		return nil, fmt.Errorf("read the node certificate: %w", err)
	}
	// The file holds the chain and the key; each parser takes its own blocks.
	cred, err := pki.VerifyNode(string(data), string(data), n.osCA, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("load the node certificate: %w", err)
	}
	n.current.Store(&cred)
	return n, nil
}

// Current returns the certificate served now.
func (n *NodeCertificate) Current() *pki.NodeCredential {
	return n.current.Load()
}

// OSCA returns the PEM certificate of the OS CA.
func (n *NodeCertificate) OSCA() string {
	return n.osCA
}

// Fingerprint returns the SHA-256 of the certificate served now.
func (n *NodeCertificate) Fingerprint() string {
	return pki.Fingerprint(n.Current().Leaf.Raw)
}

// GetCertificate serves the current certificate with its chain.
func (n *NodeCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return &n.Current().TLS, nil
}

// GetClientCertificate presents the current certificate with its chain to another node.
func (n *NodeCertificate) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return &n.Current().TLS, nil
}

// Replace switches to a new certificate, after checking that it chains through a node CA to the
// OS CA, is valid now and names the node the current one names: a node never serves another
// node's certificate. It is written to STATE before it is served, so a restart keeps it.
func (n *NodeCertificate) Replace(chain, key string, now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	cred, err := pki.VerifyNode(chain, key, n.osCA, now)
	if err != nil {
		return err
	}
	if name, want := cred.Leaf.Subject.CommonName, n.Current().Leaf.Subject.CommonName; name != want {
		return fmt.Errorf("the new node certificate is for %q, not for this node, %q", name, want)
	}
	if err := install.WriteFile(n.path, []byte(chain+key), 0o600); err != nil {
		return fmt.Errorf("record the node certificate: %w", err)
	}
	n.current.Store(&cred)
	return nil
}

// errNoCertificate is returned by a static certificate source without a certificate.
var errNoCertificate = errors.New("chalkd has no certificate to serve")

// StaticCertificate serves one certificate, as chalkd does in maintenance mode.
func StaticCertificate(cert *tls.Certificate) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if cert == nil {
			return nil, errNoCertificate
		}
		return cert, nil
	}
}
