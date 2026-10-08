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

// Bundle joins PEM certificates and bundles into one bundle, in the order given, leaving out
// repeated certificates. What follows the last PEM block of an argument is kept, so ParseBundle
// still refuses it.
func Bundle(certs ...string) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, c := range certs {
		rest := []byte(c)
		for {
			block, r := pem.Decode(rest)
			if block == nil {
				break
			}
			rest = r
			encoded := string(pem.EncodeToMemory(block))
			if !seen[encoded] {
				seen[encoded] = true
				b.WriteString(encoded)
			}
		}
		if trailing := bytes.TrimSpace(rest); len(trailing) > 0 {
			b.Write(append(trailing, '\n'))
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
