package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"
)

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
	template, err := newTemplate(names.CommonName, []string{string(RoleNode)}, now, LeafValidity)
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
// through a node CA to an OS CA of the bundle osCA for TLS servers and clients, at the time
// given; at zero checks no dates. Errors never hold the key.
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
	osCAs, err := ParseBundle(osCA)
	if err != nil {
		return NodeCredential{}, fmt.Errorf("the OS CA: %w", err)
	}
	if !IsNodeCA(nodeCA) {
		return NodeCredential{}, errors.New("the node certificate's issuer is not a node CA")
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	for _, root := range osCAs {
		roots.AddCert(root)
	}
	intermediates.AddCert(nodeCA)
	if at.IsZero() {
		at = leaf.NotBefore
	}
	// The node CA's signature on the leaf is what makes it a node certificate: a leaf the OS CA
	// signed itself verifies without the node CA that follows it, so every chain must run through
	// that node CA.
	if err := leaf.CheckSignatureFrom(nodeCA); err != nil {
		return NodeCredential{}, errors.New("the node certificate is not signed by the node CA that follows it")
	}
	for _, usage := range nodeCAUsages {
		chains, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage}, CurrentTime: at})
		if err != nil {
			return NodeCredential{}, fmt.Errorf("the node certificate does not verify against the OS CA: %w", err)
		}
		for _, chain := range chains {
			if len(chain) != 3 || !chain[1].Equal(nodeCA) || !slices.ContainsFunc(osCAs, chain[2].Equal) {
				return NodeCredential{}, errors.New("the node certificate does not chain through its node CA to the OS CA")
			}
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

// ValidateNodeCA checks that nodeCA, with its key, is a node CA that an OS CA of the bundle osCA
// issued, and that this OS CA may issue it. Dates are not checked: an expired node CA is
// replaced, not refused.
func ValidateNodeCA(nodeCA CertKey, osCA string) error {
	if err := ValidateCA(nodeCA); err != nil {
		return err
	}
	return verifyNodeCA(nodeCA.Certificate, osCA)
}

// verifyNodeCA checks the certificate of a node CA against the bundle of OS CAs.
func verifyNodeCA(nodeCA, osCA string) error {
	cert, err := ParseCertificate([]byte(nodeCA))
	if err != nil {
		return err
	}
	roots, err := ParseBundle(osCA)
	if err != nil {
		return fmt.Errorf("the OS CA: %w", err)
	}
	if !IsNodeCA(cert) {
		return errors.New("not a node CA: it must be a CA for TLS servers and clients that issues no CA")
	}
	pool := x509.NewCertPool()
	issuers := 0
	for _, root := range roots {
		// A root that issues leaves only would refuse every node certificate's chain.
		if !root.MaxPathLenZero {
			pool.AddCert(root)
			issuers++
		}
	}
	if issuers == 0 {
		return errors.New("the OS CA issues no intermediate CA, so it cannot have a node CA")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: nodeCAUsages, CurrentTime: cert.NotBefore}); err != nil {
		return fmt.Errorf("does not verify against the OS CA: %w", err)
	}
	return nil
}
