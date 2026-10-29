// Helpers for the certificates chalkd and its plugins report in Status.

package layer

import (
	"crypto/x509"
	"os"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/pki"
)

// CAWarning is how long before they expire CAs are reported: a year.
const CAWarning = 365 * 24 * time.Hour

// CertificateStatus describes a certificate with its fingerprint and, when known, its issuer's.
func CertificateStatus(name string, cert, issuer *x509.Certificate, problem string) *nodev1.CertificateStatus {
	st := &nodev1.CertificateStatus{Name: name, NotAfter: timestamppb.New(cert.NotAfter), Problem: problem, Fingerprint: pki.Fingerprint(cert.Raw)}
	if issuer != nil {
		st.Issuer = pki.Fingerprint(issuer.Raw)
	}
	return st
}

// IssuerIn returns the certificate of cas that signed cert; nil when none did.
func IssuerIn(cert *x509.Certificate, cas []*x509.Certificate) *x509.Certificate {
	for _, ca := range cas {
		if cert.CheckSignatureFrom(ca) == nil {
			return ca
		}
	}
	return nil
}

// LeafProblem warns once less than a third of a certificate's lifetime remains, when it is due
// for renewal.
func LeafProblem(cert *x509.Certificate, now time.Time) string {
	switch {
	case !now.Before(cert.NotAfter):
		return "expired"
	case !now.Before(pki.RenewAt(cert)):
		return "less than a third of its lifetime remains"
	}
	return ""
}

// CAProblem warns when a CA has less than warning left.
func CAProblem(cert *x509.Certificate, now time.Time, warning time.Duration) string {
	switch {
	case !now.Before(cert.NotAfter):
		return "expired " + cert.NotAfter.UTC().Format(time.DateOnly)
	case cert.NotAfter.Sub(now) < warning:
		return "expires " + cert.NotAfter.UTC().Format(time.DateOnly)
	}
	return ""
}

// ReadPEMCertificate reads the first certificate of a PEM file, such as one that holds its
// key too.
func ReadPEMCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pki.FirstCertificate(data)
}
