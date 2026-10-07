// Package pki issues the certificates chalkos nodes and clients authenticate with, and holds the
// cluster secrets file they are issued from.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"time"
)

// Roles a client certificate grants through its Organization, from most to least privileged.
// RoleNode marks node certificates, which grant no client role.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleReader   = "reader"
	RoleNode     = "node"
)

// Validity of what chalkos issues.
const (
	CAValidity   = 10 * 365 * 24 * time.Hour
	LeafValidity = 365 * 24 * time.Hour
)

// clockSkew backdates certificates so a node whose clock lags slightly behind still accepts them.
const clockSkew = time.Hour

// CertKey is a certificate with its private key, both PEM-encoded as they are stored.
type CertKey struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key,omitempty"`
}

// Parse decodes the certificate and the key.
func (c CertKey) Parse() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := ParseCertificate([]byte(c.Certificate))
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode([]byte(c.Key))
	if block == nil {
		return nil, nil, errors.New("no PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("private key is a %T, want ECDSA", parsed)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, nil, errors.New("private key does not belong to the certificate")
	}
	return cert, key, nil
}

// ParseCertificate decodes one PEM certificate.
func ParseCertificate(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// NewCA creates a self-signed ECDSA P-256 CA valid for ten years.
func NewCA(commonName string, now time.Time) (CertKey, error) {
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
	template.MaxPathLenZero = true
	template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	return sign(template, template, key, key)
}

// IssueClient issues a client certificate whose Organization grants role.
func IssueClient(ca CertKey, name, role string, now time.Time) (CertKey, error) {
	if !slices.Contains([]string{RoleAdmin, RoleOperator, RoleReader}, role) {
		return CertKey{}, fmt.Errorf("unknown role %q", role)
	}
	template, err := newTemplate(name, []string{role}, now, LeafValidity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	return issue(ca, template)
}

// IssueNode issues the certificate chalkd serves on an installed node, valid for the given host
// names and addresses.
func IssueNode(ca CertKey, name string, dnsNames []string, ips []net.IP, now time.Time) (CertKey, error) {
	template, err := newTemplate(name, []string{RoleNode}, now, LeafValidity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	template.DNSNames = dnsNames
	template.IPAddresses = ips
	return issue(ca, template)
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
	return issue(ca, template)
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

// Fingerprint returns the SHA-256 of a DER certificate in lower-case hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint accepts a fingerprint with colons, spaces or upper-case letters, as
// tools print them, and an optional "sha256:" prefix.
func NormalizeFingerprint(fp string) string {
	fp = strings.ToLower(strings.TrimSpace(fp))
	fp = strings.TrimPrefix(fp, "sha256:")
	return strings.NewReplacer(":", "", " ", "").Replace(fp)
}

// Role returns the most privileged client role a certificate's Organization grants; ok is
// false when it grants none.
func Role(cert *x509.Certificate) (role string, ok bool) {
	for _, r := range []string{RoleAdmin, RoleOperator, RoleReader} {
		if slices.Contains(cert.Subject.Organization, r) {
			return r, true
		}
	}
	return "", false
}

// Allows reports whether a client with role may call something that requires the role need. An
// unrecognised need fails closed: Allows reports false rather than letting every role through.
func Allows(role, need string) bool {
	rank := map[string]int{RoleReader: 1, RoleOperator: 2, RoleAdmin: 3}
	r, ok := rank[need]
	return ok && rank[role] >= r
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

func issue(ca CertKey, template *x509.Certificate) (CertKey, error) {
	caCert, caKey, err := ca.Parse()
	if err != nil {
		return CertKey{}, fmt.Errorf("CA: %w", err)
	}
	if template.NotAfter.After(caCert.NotAfter) {
		template.NotAfter = caCert.NotAfter
	}
	// Backdating must not start a certificate before its CA, or it never verifies at its start.
	if template.NotBefore.Before(caCert.NotBefore) {
		template.NotBefore = caCert.NotBefore
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertKey{}, err
	}
	return sign(template, caCert, key, caKey)
}

func sign(template, parent *x509.Certificate, key, parentKey *ecdsa.PrivateKey) (CertKey, error) {
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		return CertKey{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return CertKey{}, err
	}
	return CertKey{
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Key:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}, nil
}
