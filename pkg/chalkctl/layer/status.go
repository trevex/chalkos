package layer

import (
	"fmt"
	"slices"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/pki"
)

// Trusts checks that a node's status reports the entry of name with exactly the fingerprints
// want and, when issuing is given, that value as the one that issues.
func Trusts(st *nodev1.StatusResponse, name string, want []string, issuing string) error {
	for _, t := range st.Trust {
		if t.Name != name {
			continue
		}
		if !sameFingerprints(t.Fingerprints, want) {
			return fmt.Errorf("trusts %d %s values, not the %d of the secrets file", len(t.Fingerprints), name, len(want))
		}
		if issuing != "" && t.Issuing != issuing {
			return fmt.Errorf("issues with another %s than the secrets file's", name)
		}
		return nil
	}
	return fmt.Errorf("reports no %s", name)
}

// sameFingerprints reports whether a and b hold the same fingerprints, in any order.
func sameFingerprints(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// CertificateOf returns the status of the node's certificate of the name.
func CertificateOf(st *nodev1.StatusResponse, name string) *nodev1.CertificateStatus {
	for _, c := range st.Certificates {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// IssuedBy checks that the node's certificate of the name was issued by the CA of the
// fingerprint.
func IssuedBy(st *nodev1.StatusResponse, name, issuer string) error {
	c := CertificateOf(st, name)
	switch {
	case c == nil:
		return fmt.Errorf("reports no %s certificate", name)
	case c.Issuer != issuer:
		return fmt.Errorf("its %s certificate is not issued by the CA that issues now", name)
	}
	return nil
}

// FingerprintOf is the fingerprint of a PEM certificate.
func FingerprintOf(certificate string) (string, error) {
	cert, err := pki.ParseCertificate([]byte(certificate))
	if err != nil {
		return "", err
	}
	return pki.Fingerprint(cert.Raw), nil
}
