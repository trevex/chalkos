package client

import (
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/pki"
)

// Verifier checks the certificate a node presents. ByFingerprint, AnyCertificate, ByCA,
// AnyNodeOf and ByCAIgnoringValidity make one.
type Verifier interface {
	// check refuses a Verifier that would verify nothing it should.
	check() error
	// verify checks the node's chain, its certificate first, whose fingerprint is fp.
	verify(chain []*x509.Certificate, fp string) error
}

// ByFingerprint pins the node's certificate by its SHA-256, as a node in maintenance mode, which
// serves a self-signed certificate, is verified.
func ByFingerprint(fp string) Verifier {
	return fingerprint(pki.NormalizeFingerprint(fp))
}

type fingerprint string

func (f fingerprint) check() error {
	if f == "" {
		return errors.New("verifying the node by its fingerprint needs the fingerprint")
	}
	return nil
}

func (f fingerprint) verify(_ []*x509.Certificate, fp string) error {
	if fp != string(f) {
		return fmt.Errorf("the node's certificate has the fingerprint %s, not %s", fp, f)
	}
	return nil
}

// AnyCertificate accepts any certificate; Conn.Fingerprint reports what the node presented.
func AnyCertificate() Verifier {
	return anyCertificate{}
}

type anyCertificate struct{}

func (anyCertificate) check() error { return nil }

func (anyCertificate) verify([]*x509.Certificate, string) error { return nil }

// ByCA verifies the certificate of an installed node: it must chain to the CA and be valid for
// the node's name. The CA issues every node's certificate; only the name tells this node from the
// others.
func ByCA(ca *x509.CertPool, name string) Verifier {
	return byCA{ca: ca, name: name}
}

// AnyNodeOf accepts any node certificate that verifies against the CA through a node CA, as a
// node that reaches a control plane through an address that names no node, such as a VIP, does.
func AnyNodeOf(ca *x509.CertPool) Verifier {
	return byCA{ca: ca, anyNode: true}
}

// ByCAIgnoringValidity verifies like ByCA, as of the node certificate's start, so that a node
// whose certificate expired is reached to deliver it a new one. Nothing else may use it.
func ByCAIgnoringValidity(ca *x509.CertPool, name string) Verifier {
	return byCA{ca: ca, name: name, ignoreValidity: true}
}

type byCA struct {
	ca             *x509.CertPool
	name           string
	anyNode        bool
	ignoreValidity bool
}

func (v byCA) check() error {
	switch {
	case v.ca == nil:
		return errors.New("verifying the node by the CA needs the CA")
	case v.name == "" && !v.anyNode:
		return errors.New("verifying the node by the CA needs the node's name")
	}
	return nil
}

func (v byCA) verify(chain []*x509.Certificate, _ string) error {
	leaf := chain[0]
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	opts := x509.VerifyOptions{Roots: v.ca, Intermediates: intermediates, DNSName: v.name,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if v.ignoreValidity {
		// Each CA's validity covers the start of what it issued.
		opts.CurrentTime = leaf.NotBefore
	}
	chains, err := leaf.Verify(opts)
	if err != nil {
		return err
	}
	if role, _ := pki.ClientRole(chains); v.anyNode && role != pki.RoleNode {
		return errors.New("the peer's certificate is not a node certificate")
	}
	return nil
}
