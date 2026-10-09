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
