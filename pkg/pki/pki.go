// Package pki issues the certificates chalkos nodes and clients authenticate with, and holds the
// cluster secrets file they are issued from.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
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
// RoleNode is the role of every certificate the node CA issued, whatever its Organization; it is
// none of the others.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleReader   = "reader"
	RoleNode     = "node"
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

// nodeCAUsages are the only extended key usages of the node CA. Chain verification requires
// every certificate of a chain to allow the usage asked for, so nothing the node CA issues
// verifies for anything but TLS servers and clients.
var nodeCAUsages = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}

// NewNodeCA issues the node CA from the OS CA: an intermediate CA, valid for five years and never
// beyond the OS CA, that issues node certificates and nothing below it.
func NewNodeCA(osCA CertKey, now time.Time) (CertKey, error) {
	template, err := newTemplate("chalkos node CA", nil, now, NodeCAValidity)
	if err != nil {
		return CertKey{}, err
	}
	template.IsCA = true
	template.BasicConstraintsValid = true
	template.MaxPathLenZero = true
	template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	template.ExtKeyUsage = nodeCAUsages
	return issue(osCA, template, now)
}

// IsNodeCA reports whether cert is a node CA: a CA that issues no CA below it, for TLS servers
// and clients only.
func IsNodeCA(cert *x509.Certificate) bool {
	return cert.IsCA && cert.BasicConstraintsValid && cert.MaxPathLenZero && cert.MaxPathLen == 0 &&
		slices.Equal(cert.ExtKeyUsage, nodeCAUsages) && len(cert.UnknownExtKeyUsage) == 0
}

// IssueClient issues a client certificate of the OS CA whose Organization grants role, valid
// for validity and never beyond the CA.
func IssueClient(osCA CertKey, name, role string, validity time.Duration, now time.Time) (CertKey, error) {
	if !slices.Contains([]string{RoleAdmin, RoleOperator, RoleReader}, role) {
		return CertKey{}, fmt.Errorf("unknown role %q", role)
	}
	if validity <= 0 {
		return CertKey{}, errors.New("a client certificate needs a validity")
	}
	template, err := newTemplate(name, []string{role}, now, validity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	return issue(osCA, template, now)
}

// NodeNames are what a node certificate is for: the node's name, which chalkctl verifies a node
// by, and the host names and addresses it is reached at.
type NodeNames struct {
	CommonName string
	DNSNames   []string
	IPs        []net.IP
}

// NamesOf returns the names a node certificate is for.
func NamesOf(cert *x509.Certificate) NodeNames {
	return NodeNames{CommonName: cert.Subject.CommonName, DNSNames: cert.DNSNames, IPs: cert.IPAddresses}
}

// Equal reports whether both name the same, in the same order.
func (n NodeNames) Equal(o NodeNames) bool {
	return n.CommonName == o.CommonName && slices.Equal(n.DNSNames, o.DNSNames) &&
		slices.EqualFunc(n.IPs, o.IPs, func(a, b net.IP) bool { return a.Equal(b) })
}

// IssueNode issues a node certificate with a new key from the node CA, for TLS servers and
// clients: chalkd serves it, and presents it to renew it. Certificate holds the chain chalkd
// presents, the node certificate followed by the node CA's.
func IssueNode(nodeCA CertKey, names NodeNames, now time.Time) (CertKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertKey{}, err
	}
	chain, err := SignNode(nodeCA, names, &key.PublicKey, now)
	if err != nil {
		return CertKey{}, err
	}
	keyPEM, err := EncodeKey(key)
	if err != nil {
		return CertKey{}, err
	}
	return CertKey{Certificate: chain, Key: keyPEM}, nil
}

// SignNode issues a node certificate for pub, whose key the node keeps, and returns the chain
// IssueNode returns. It refuses a CA that is not a node CA: only a chain through the node CA
// makes a certificate a node's.
func SignNode(nodeCA CertKey, names NodeNames, pub crypto.PublicKey, now time.Time) (string, error) {
	caCert, err := ParseCertificate([]byte(nodeCA.Certificate))
	if err != nil {
		return "", fmt.Errorf("node CA: %w", err)
	}
	if !IsNodeCA(caCert) {
		return "", errors.New("node certificates are issued by the node CA only")
	}
	if names.CommonName == "" {
		return "", errors.New("a node certificate needs the node's name")
	}
	template, err := newTemplate(names.CommonName, []string{RoleNode}, now, LeafValidity)
	if err != nil {
		return "", err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	template.DNSNames = names.DNSNames
	template.IPAddresses = names.IPs
	der, err := issueFor(nodeCA, template, pub, now)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + nodeCA.Certificate, nil
}

// NodeCredential is a node certificate whose chain verified.
type NodeCredential struct {
	// Leaf is the node certificate; TLS holds it, the node CA's certificate and the key.
	Leaf *x509.Certificate
	TLS  tls.Certificate
}

// VerifyNode checks that a PEM chain and key are a node certificate with its key that chains
// through a node CA to the OS CA for TLS servers and clients, at the time given; at zero checks
// no dates. Errors never hold the key.
func VerifyNode(chain, key, osCA string, at time.Time) (NodeCredential, error) {
	pair, err := tls.X509KeyPair([]byte(chain), []byte(key))
	if err != nil {
		return NodeCredential{}, fmt.Errorf("the node certificate and its key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return NodeCredential{}, err
	}
	pair.Leaf = leaf
	if len(pair.Certificate) != 2 {
		return NodeCredential{}, fmt.Errorf("the node certificate's chain holds %d certificates, want it and the node CA's", len(pair.Certificate))
	}
	nodeCA, err := x509.ParseCertificate(pair.Certificate[1])
	if err != nil {
		return NodeCredential{}, err
	}
	root, err := ParseCertificate([]byte(osCA))
	if err != nil {
		return NodeCredential{}, fmt.Errorf("the OS CA: %w", err)
	}
	if !IsNodeCA(nodeCA) {
		return NodeCredential{}, errors.New("the node certificate's issuer is not a node CA")
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	intermediates.AddCert(nodeCA)
	if at.IsZero() {
		at = leaf.NotBefore
	}
	for _, usage := range nodeCAUsages {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage}, CurrentTime: at}); err != nil {
			return NodeCredential{}, fmt.Errorf("the node certificate does not verify against the OS CA: %w", err)
		}
	}
	return NodeCredential{Leaf: leaf, TLS: pair}, nil
}

// NodeFile encodes a verified node certificate as the one PEM file a node keeps it in: the chain,
// then the key. It is built from the parsed certificates and key, never from the bytes they
// arrived as, which may lack a final newline or carry other blocks that the joined file would
// misread. The file is checked as it is loaded, without dates, before it is returned.
func NodeFile(cred NodeCredential, osCA string) ([]byte, error) {
	var file []byte
	for _, der := range cred.TLS.Certificate {
		file = append(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	der, err := x509.MarshalPKCS8PrivateKey(cred.TLS.PrivateKey)
	if err != nil {
		return nil, errors.New("encode the node certificate's key")
	}
	file = append(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})...)
	loaded, err := VerifyNode(string(file), string(file), osCA, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("the encoded node certificate: %w", err)
	}
	if !loaded.Leaf.Equal(cred.Leaf) {
		return nil, errors.New("the encoded node certificate is not the one verified")
	}
	return file, nil
}

// RenewAt is when a certificate is renewed: once two thirds of its lifetime have passed.
func RenewAt(cert *x509.Certificate) time.Time {
	return cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * 2 / 3)
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

// ClientRole derives the role of a client from the chains its certificate verified with. A chain
// with a certificate between the client's and the root passes through the node CA, the only
// intermediate the OS CA issues, so the client is a node whatever its Organization says: control
// planes hold the node CA and must not be able to issue anything else. A certificate the root
// issued directly gets the role of its Organization.
func ClientRole(chains [][]*x509.Certificate) (role string, ok bool) {
	if len(chains) == 0 {
		return "", false
	}
	for _, chain := range chains {
		if len(chain) > 2 {
			return RoleNode, true
		}
	}
	for _, chain := range chains {
		if len(chain) != 2 {
			return "", false
		}
	}
	return Role(chains[0][0])
}

// Allows reports whether a client with role may call something that requires the role need. The
// node role is apart from the others: it allows only what needs it, and only it allows that. An
// unrecognised need fails closed: Allows reports false rather than letting every role through.
func Allows(role, need string) bool {
	if role == RoleNode || need == RoleNode {
		return role == need && role != ""
	}
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

// EncodeKey encodes a private key as PKCS #8 PEM, as chalkos stores keys.
func EncodeKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}
