package pki

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// A bundle is one or more PEM certificates in one file, as nodes trust them while a CA rotates:
// the CA that issues now first, then those still trusted.

// ParseBundle decodes every certificate of a bundle. Anything but certificates between them is
// refused, so a key that ended up in a bundle by mistake is never passed on as trust.
func ParseBundle(bundle string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(bundle)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
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
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("the bundle holds data that is not PEM")
	}
	if len(certs) == 0 {
		return nil, errors.New("no PEM certificate")
	}
	return certs, nil
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

// Bundle joins PEM certificates into a bundle, in the order given, leaving out repeats.
func Bundle(certs ...string) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, c := range certs {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		b.WriteString(c + "\n")
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
