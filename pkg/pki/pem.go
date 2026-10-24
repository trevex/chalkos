package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ParseCertificate decodes one PEM certificate.
func ParseCertificate(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
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

// EncodeKey encodes a private key as PKCS #8 PEM, as chalkos stores keys.
func EncodeKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// ParseECKey decodes a PEM PKCS#8 ECDSA P-256 private key.
func ParseECKey(data string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(data))
	if block == nil {
		return nil, errors.New("no PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("the private key is not ECDSA P-256")
	}
	return key, nil
}

// ServiceAccountPublicKey returns the PEM public key of a PEM private service-account key.
func ServiceAccountPublicKey(private string) (string, error) {
	key, err := ParseECKey(private)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// ParsePublicKey decodes a PEM ECDSA P-256 public key, as service accounts' are.
func ParsePublicKey(data string) (*ecdsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(data))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("not one PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("the public key is not ECDSA P-256")
	}
	return key, nil
}

// PublicKeyFingerprint is the SHA-256 of a PEM public key's DER form.
func PublicKeyFingerprint(data string) (string, error) {
	key, err := ParsePublicKey(data)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// ValidateCA checks that a certificate and key form a CA that may sign certificates.
func ValidateCA(ca CertKey) error {
	cert, _, err := ca.Parse()
	if err != nil {
		return err
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return errors.New("not a CA certificate")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("the CA certificate lacks the certificate signing key usage")
	}
	return nil
}

// NamedCA is a CA with the name errors call it by.
type NamedCA struct {
	Name string
	CA   CertKey
}

// RequireDistinctCAs checks that no two CAs share a public key. The CAs grant different
// access: an etcd CA that is also the Kubernetes CA would give every certificate the Kubernetes
// CA issues full access to etcd.
func RequireDistinctCAs(cas ...NamedCA) error {
	keys := make([][]byte, len(cas))
	for i, ca := range cas {
		cert, err := ParseCertificate([]byte(ca.CA.Certificate))
		if err != nil {
			return fmt.Errorf("%s: %w", ca.Name, err)
		}
		keys[i] = cert.RawSubjectPublicKeyInfo
		for j := range i {
			if bytes.Equal(keys[j], keys[i]) {
				return fmt.Errorf("%s and %s share a key; every CA needs its own", cas[j].Name, ca.Name)
			}
		}
	}
	return nil
}

// A bundle is one or more PEM certificates in one file, as nodes trust them while a CA rotates:
// the CA that issues now first, then those still trusted.

// ParseBundle decodes every certificate of a bundle. Anything but certificates and the whitespace
// around them is refused, so a key that ended up in a bundle by mistake is never passed on as
// trust.
func ParseBundle(bundle string) ([]*x509.Certificate, error) {
	blocks, ok := pemBlocks([]byte(bundle))
	var certs []*x509.Certificate
	for _, block := range blocks {
		if block.Type != "CERTIFICATE" {
			// The block is not echoed: it may be a key.
			return nil, fmt.Errorf("the bundle holds a %s block where certificates are expected", blockType(block.Type))
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	if !ok {
		return nil, errors.New("the bundle holds data that is not PEM")
	}
	if len(certs) == 0 {
		return nil, errors.New("no PEM certificate")
	}
	return certs, nil
}

// pemBlocks decodes the PEM blocks of data in order. ok is false when data holds anything else
// than blocks and whitespace: pem.Decode alone skips text before a block, and a block it cannot
// decode, without a trace.
func pemBlocks(data []byte) (blocks []*pem.Block, ok bool) {
	rest := data
	for {
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) == 0 {
			return blocks, true
		}
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return blocks, false
		}
		block, r := pem.Decode(rest)
		if block == nil || bytes.Count(rest[:len(rest)-len(r)], []byte("-----BEGIN ")) != 1 {
			return blocks, false
		}
		blocks = append(blocks, block)
		rest = r
	}
}

// blockType names a PEM block's type without echoing anything that may be secret.
func blockType(t string) string {
	if strings.Contains(t, "PRIVATE KEY") {
		return "private key"
	}
	return fmt.Sprintf("%q", t)
}

// BundlePool parses a bundle into a pool of roots.
func BundlePool(bundle string) (*x509.CertPool, error) {
	certs, err := ParseBundle(bundle)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return pool, nil
}

// Bundle joins PEM certificates and bundles into one bundle, in the order given, leaving out
// repeated certificates. An argument holding anything but PEM blocks is kept as it is, so
// ParseBundle still refuses the result.
func Bundle(certs ...string) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, c := range certs {
		blocks, ok := pemBlocks([]byte(c))
		if !ok {
			b.WriteString(strings.TrimSpace(c) + "\n")
			continue
		}
		for _, block := range blocks {
			encoded := string(pem.EncodeToMemory(block))
			if !seen[encoded] {
				seen[encoded] = true
				b.WriteString(encoded)
			}
		}
	}
	return b.String()
}

// Fingerprints returns the fingerprint of every certificate of a bundle, in its order.
func Fingerprints(bundle string) ([]string, error) {
	certs, err := ParseBundle(bundle)
	if err != nil {
		return nil, err
	}
	fps := make([]string, len(certs))
	for i, cert := range certs {
		fps[i] = Fingerprint(cert.Raw)
	}
	return fps, nil
}

// ValidateOSCABundle checks that every certificate of a bundle of OS CAs is a self-signed CA's
// that may sign certificates and no node CA's, and returns them. A node CA trusted as a root
// would make what control planes issue verify as the OS CA's own client certificates.
func ValidateOSCABundle(bundle string) ([]*x509.Certificate, error) {
	certs, err := ParseBundle(bundle)
	if err != nil {
		return nil, err
	}
	for i, cert := range certs {
		if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("certificate %d of the bundle, %s, is not a CA's", i+1, cert.Subject.CommonName)
		}
		if IsNodeCA(cert) || cert.CheckSignatureFrom(cert) != nil {
			return nil, fmt.Errorf("certificate %d of the bundle, %s, is not a self-signed root CA's", i+1, cert.Subject.CommonName)
		}
	}
	return certs, nil
}
