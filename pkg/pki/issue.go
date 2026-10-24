// Package pki issues the certificates chalkos nodes and clients authenticate with, and checks,
// encodes and bundles them. The cluster secrets file they are issued from is clustersecrets.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Validity of what chalkos issues.
const (
	CAValidity     = 10 * 365 * 24 * time.Hour
	NodeCAValidity = 5 * 365 * 24 * time.Hour
	LeafValidity   = 365 * 24 * time.Hour
)

// clockSkew backdates certificates so a node whose clock lags slightly behind still accepts them.
const clockSkew = time.Hour

// CertKey is a certificate with its private key, both PEM-encoded as they are stored.
type CertKey struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key,omitempty"`
}

// String names the certificate's subject and expiry and never prints the key, so logging a
// CertKey or a struct holding one does not leak it.
func (c CertKey) String() string {
	key := "none"
	if c.Key != "" {
		key = "redacted"
	}
	cert, err := ParseCertificate([]byte(c.Certificate))
	if err != nil {
		return fmt.Sprintf("pki.CertKey{certificate: invalid, key: %s}", key)
	}
	return fmt.Sprintf("pki.CertKey{subject: %s, notAfter: %s, key: %s}", cert.Subject, cert.NotAfter.UTC().Format(time.RFC3339), key)
}

// GoString redacts a CertKey like String.
func (c CertKey) GoString() string {
	return c.String()
}

// Parse decodes the certificate and the key, which must be ECDSA P-256 and belong to it.
func (c CertKey) Parse() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := ParseCertificate([]byte(c.Certificate))
	if err != nil {
		return nil, nil, err
	}
	key, err := ParseECKey(c.Key)
	if err != nil {
		return nil, nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, nil, errors.New("private key does not belong to the certificate")
	}
	return cert, key, nil
}

// NewCA creates a self-signed ECDSA P-256 CA valid for ten years that issues leaf certificates
// only.
func NewCA(commonName string, now time.Time) (CertKey, error) {
	return newRootCA(commonName, 0, now)
}

// NewOSCA creates the OS CA: a self-signed ECDSA P-256 CA valid for ten years, which issues
// client certificates and the node CA.
func NewOSCA(now time.Time) (CertKey, error) {
	return newRootCA("chalkos OS CA", 1, now)
}

func newRootCA(commonName string, maxPathLen int, now time.Time) (CertKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertKey{}, err
	}
	template, err := newTemplate(commonName, nil, now, CAValidity)
	if err != nil {
		return CertKey{}, err
	}
	template.IsCA = true
	template.BasicConstraintsValid = true
	template.MaxPathLen = maxPathLen
	template.MaxPathLenZero = maxPathLen == 0
	template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	return sign(template, template, key, key)
}

// RenewAt is when a certificate is renewed: once two thirds of its lifetime have passed.
func RenewAt(cert *x509.Certificate) time.Time {
	return cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * 2 / 3)
}

// IssuedAt is when a CA chalkos made was made: its NotBefore is backdated by the clock skew.
func IssuedAt(ca *x509.Certificate) time.Time {
	return ca.NotBefore.Add(clockSkew)
}

// Leaf describes a certificate IssueLeaf issues.
type Leaf struct {
	CommonName   string
	Organization []string
	DNSNames     []string
	IPs          []net.IP
	// Server and Client select the extended key usages.
	Server, Client bool
	// Validity defaults to LeafValidity; the CA's own expiry caps it.
	Validity time.Duration
}

// IssueLeaf issues a certificate from ca with a new ECDSA P-256 key.
func IssueLeaf(ca CertKey, l Leaf, now time.Time) (CertKey, error) {
	if !l.Server && !l.Client {
		return CertKey{}, errors.New("a leaf certificate must allow server or client authentication")
	}
	validity := l.Validity
	if validity == 0 {
		validity = LeafValidity
	}
	template, err := newTemplate(l.CommonName, l.Organization, now, validity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	if l.Server {
		template.ExtKeyUsage = append(template.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
	}
	if l.Client {
		template.ExtKeyUsage = append(template.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	}
	template.DNSNames = l.DNSNames
	template.IPAddresses = l.IPs
	return issue(ca, template, now)
}

// SelfSigned creates the certificate chalkd serves in maintenance mode, before the node has one
// from the OS CA. Clients pin it by its fingerprint.
func SelfSigned(name string, now time.Time) (CertKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertKey{}, err
	}
	template, err := newTemplate(name, nil, now, LeafValidity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	return sign(template, template, key, key)
}

func newTemplate(commonName string, organization []string, now time.Time, validity time.Duration) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	return &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: organization},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(validity),
	}, nil
}

func issue(ca CertKey, template *x509.Certificate, now time.Time) (CertKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertKey{}, err
	}
	der, err := issueFor(ca, template, &key.PublicKey, now)
	if err != nil {
		return CertKey{}, err
	}
	keyPEM, err := EncodeKey(key)
	if err != nil {
		return CertKey{}, err
	}
	return CertKey{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Key: keyPEM}, nil
}

// issueFor signs a certificate for pub with ca, never beyond the CA's validity, and returns it
// in DER.
func issueFor(ca CertKey, template *x509.Certificate, pub crypto.PublicKey, now time.Time) ([]byte, error) {
	caCert, caKey, err := ca.Parse()
	if err != nil {
		return nil, fmt.Errorf("CA: %w", err)
	}
	if template.NotAfter.After(caCert.NotAfter) {
		template.NotAfter = caCert.NotAfter
	}
	// Backdating must not start a certificate before its CA, or it never verifies at its start.
	if template.NotBefore.Before(caCert.NotBefore) {
		template.NotBefore = caCert.NotBefore
	}
	// Backdating would still leave a window, already past, under a CA that expired less than
	// clockSkew ago.
	if !caCert.NotAfter.After(now) {
		return nil, fmt.Errorf("the CA expired %s", caCert.NotAfter.UTC().Format(time.RFC3339))
	}
	if !template.NotAfter.After(template.NotBefore) {
		return nil, fmt.Errorf("the certificate would never be valid: the CA is valid from %s until %s",
			caCert.NotBefore.UTC().Format(time.RFC3339), caCert.NotAfter.UTC().Format(time.RFC3339))
	}
	return x509.CreateCertificate(rand.Reader, template, caCert, pub, caKey)
}

func sign(template, parent *x509.Certificate, key, parentKey *ecdsa.PrivateKey) (CertKey, error) {
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		return CertKey{}, err
	}
	keyPEM, err := EncodeKey(key)
	if err != nil {
		return CertKey{}, err
	}
	return CertKey{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Key: keyPEM}, nil
}
