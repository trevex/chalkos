package chalkd

import (
	"crypto/tls"
	"crypto/x509"
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

// NodeCertificate is the certificate an installed node's chalkd serves and presents to renew it,
// with the OS CAs the node trusts. Both are replaced while chalkd runs: connections made
// afterwards get the new ones.
type NodeCertificate struct {
	// path holds the chain and the key in one file, so a replacement switches both at once;
	// caPath holds the OS CAs.
	path, caPath string

	// mu serialises replacements; current and trust are read without it.
	mu      sync.Mutex
	current atomic.Pointer[pki.NodeCredential]
	trust   atomic.Pointer[trust]
}

// trust is a bundle of OS CAs, the one that issues first, as a pool too.
type trust struct {
	bundle string
	pool   *x509.CertPool
}

func newTrust(bundle string) (*trust, error) {
	if _, err := pki.ValidateOSCABundle(bundle); err != nil {
		return nil, fmt.Errorf("the OS CA: %w", err)
	}
	pool, err := pki.BundlePool(bundle)
	if err != nil {
		return nil, err
	}
	return &trust{bundle: bundle, pool: pool}, nil
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
	t, err := newTrust(string(osCA))
	if err != nil {
		return nil, err
	}
	n := &NodeCertificate{path: filepath.Join(dir, NodeCertificateFile), caPath: filepath.Join(dir, CAFile)}
	n.trust.Store(t)
	// A crash in the middle of a replacement leaves its temporary file, which holds a key.
	stale, _ := filepath.Glob(filepath.Join(dir, "."+NodeCertificateFile+".*"))
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove an interrupted write of the node certificate: %w", err)
		}
	}
	data, err := os.ReadFile(n.path)
	if err != nil {
		return nil, fmt.Errorf("read the node certificate: %w", err)
	}
	// The file holds the chain and the key; each parser takes its own blocks.
	cred, err := pki.VerifyNode(string(data), string(data), t.bundle, time.Time{})
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

// OSCA returns the PEM certificates of the OS CAs the node trusts, the one that issues first.
func (n *NodeCertificate) OSCA() string {
	return n.trust.Load().bundle
}

// ClientCAs returns the OS CAs as the pool chalkd verifies clients by.
func (n *NodeCertificate) ClientCAs() *x509.CertPool {
	return n.trust.Load().pool
}

// ReplaceTrust makes the node trust the OS CAs of bundle from now on. It refuses a bundle that
// would lock the node out: one its own certificate or any of keep, such as the caller's client
// certificate, does not verify against. The bundle is written to STATE before it is used, so a
// restart keeps it.
func (n *NodeCertificate) ReplaceTrust(bundle string, keep ...*x509.Certificate) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, err := n.checkTrust(bundle, keep...)
	if err != nil {
		return err
	}
	if t.bundle == n.OSCA() {
		return nil
	}
	if err := install.WriteFile(n.caPath, []byte(t.bundle), 0o644); err != nil {
		return fmt.Errorf("record the OS CAs: %w", err)
	}
	n.trust.Store(t)
	return nil
}

// CheckTrust checks a bundle as ReplaceTrust does, without trusting it.
func (n *NodeCertificate) CheckTrust(bundle string, keep ...*x509.Certificate) error {
	_, err := n.checkTrust(bundle, keep...)
	return err
}

func (n *NodeCertificate) checkTrust(bundle string, keep ...*x509.Certificate) (*trust, error) {
	t, err := newTrust(pki.Bundle(bundle))
	if err != nil {
		return nil, err
	}
	if _, err := pki.NodeFile(*n.Current(), t.bundle); err != nil {
		return nil, fmt.Errorf("the OS CAs do not verify the node's certificate: %w", err)
	}
	for _, cert := range keep {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: t.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			return nil, fmt.Errorf("the OS CAs do not verify the certificate of %s, which would lose access: %w", cert.Subject.CommonName, err)
		}
	}
	return t, nil
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
	_, err := n.replace(nil, chain, key, now)
	return err
}

// ReplaceFrom is Replace for a certificate obtained while from was current. It changes nothing
// and reports false once from was replaced in the meantime, as by a certificate chalkctl
// delivered, which is newer than what was asked for.
func (n *NodeCertificate) ReplaceFrom(from *x509.Certificate, chain, key string, now time.Time) (bool, error) {
	return n.replace(from, chain, key, now)
}

func (n *NodeCertificate) replace(from *x509.Certificate, chain, key string, now time.Time) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if from != nil && n.Current().Leaf != from {
		return false, nil
	}
	cred, err := n.check(chain, key, now)
	if err != nil {
		return false, err
	}
	file, err := pki.NodeFile(cred, n.OSCA())
	if err != nil {
		return false, err
	}
	if err := install.WriteFile(n.path, file, 0o600); err != nil {
		return false, fmt.Errorf("record the node certificate: %w", err)
	}
	n.current.Store(&cred)
	return true, nil
}

// Check checks a new certificate as Replace does, without switching to it.
func (n *NodeCertificate) Check(chain, key string, now time.Time) error {
	_, err := n.check(chain, key, now)
	return err
}

func (n *NodeCertificate) check(chain, key string, now time.Time) (pki.NodeCredential, error) {
	cred, err := pki.VerifyNode(chain, key, n.OSCA(), now)
	if err != nil {
		return pki.NodeCredential{}, err
	}
	if name, want := cred.Leaf.Subject.CommonName, n.Current().Leaf.Subject.CommonName; name != want {
		return pki.NodeCredential{}, fmt.Errorf("the new node certificate is for %q, not for this node, %q", name, want)
	}
	return cred, nil
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
