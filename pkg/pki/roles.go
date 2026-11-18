package pki

import (
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Role is what a certificate allows its holder to call on a node.
type Role string

// The roles. RoleNode is the role of every certificate the node CA issued, whatever its
// Organization; it is none of the others.
const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleReader   Role = "reader"
	RoleNode     Role = "node"
)

// clientRoles are the roles a client certificate grants through its Organization, from most to
// least privileged.
var clientRoles = []Role{RoleAdmin, RoleOperator, RoleReader}

// IssueClient issues a client certificate of the OS CA whose Organization grants role, valid
// for validity and never beyond the CA.
func IssueClient(osCA CertKey, name string, role Role, validity time.Duration, now time.Time) (CertKey, error) {
	if !slices.Contains(clientRoles, role) {
		return CertKey{}, fmt.Errorf("unknown role %q", role)
	}
	if validity <= 0 {
		return CertKey{}, errors.New("a client certificate needs a validity")
	}
	template, err := newTemplate(name, []string{string(role)}, now, validity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	return issue(osCA, template, now)
}

// RoleOf returns the most privileged client role a certificate's Organization grants; ok is
// false when it grants none.
func RoleOf(cert *x509.Certificate) (role Role, ok bool) {
	for _, r := range clientRoles {
		if slices.Contains(cert.Subject.Organization, string(r)) {
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
func ClientRole(chains [][]*x509.Certificate) (role Role, ok bool) {
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
	return RoleOf(chains[0][0])
}

// Allows reports whether a client with the role may call something that requires the role need.
// The node role is apart from the others: it allows only what needs it, and only it allows that.
// An unrecognised need fails closed: Allows reports false rather than letting every role through.
func (r Role) Allows(need Role) bool {
	if r == RoleNode || need == RoleNode {
		return r == need
	}
	n := slices.Index(clientRoles, need)
	i := slices.Index(clientRoles, r)
	return n >= 0 && i >= 0 && i <= n
}

// VerifyClient verifies a client's certificate, presented[0], with the certificates it presented
// after it as intermediates, against roots at now, and returns its chains for ClientRole. A
// server calls it for every request: the chains TLS verified when the connection was made would
// keep a client of a root it stopped trusting, or one whose certificate expired, authorised for as
// long as it keeps the connection open.
func VerifyClient(presented []*x509.Certificate, roots *x509.CertPool, now time.Time) ([][]*x509.Certificate, error) {
	if len(presented) == 0 {
		return nil, errors.New("no client certificate")
	}
	if roots == nil {
		return nil, errors.New("no OS CA is trusted")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range presented[1:] {
		intermediates.AddCert(cert)
	}
	return presented[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}
