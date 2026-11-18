package registry

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/trevex/chalkos/pkg/pki"
)

// loadCredentials reads the node certificate's chain and key from the one file chalkd keeps them
// in, and the OS CAs that client certificates verify against. The registry reads them once: its
// unit gets them as systemd credentials, copies the files' service user may read, and restarts
// when chalkd replaces either.
func loadCredentials(certificate, ca string) (tls.Certificate, *x509.CertPool, error) {
	pair, err := tls.LoadX509KeyPair(certificate, certificate)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("the node certificate: %w", err)
	}
	bundle, err := os.ReadFile(ca)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("the OS CAs: %w", err)
	}
	roots, err := pki.BundlePool(string(bundle))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("the OS CAs: %w", err)
	}
	return pair, roots, nil
}
