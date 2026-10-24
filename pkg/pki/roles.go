package pki

import (
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Roles a client certificate grants through its Organization, from most to least privileged.
// RoleNode is the role of every certificate the node CA issued, whatever its Organization; it is
// none of the others.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleReader   = "reader"
	RoleNode     = "node"
)

// IssueClient issues a client certificate of the OS CA whose Organization grants role, valid
// for validity and never beyond the CA.
func IssueClient(osCA CertKey, name, role string, validity time.Duration, now time.Time) (CertKey, error) {
	if !slices.Contains([]string{RoleAdmin, RoleOperator, RoleReader}, role) {
		return CertKey{}, fmt.Errorf("unknown role %q", role)
	}
	if validity <= 0 {
		return CertKey{}, errors.New("a client certificate needs a validity")
	}
	template, err := newTemplate(name, []string{role}, now, validity)
	if err != nil {
		return CertKey{}, err
	}
	template.KeyUsage = x509.KeyUsageDigitalSignature
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	return issue(osCA, template, now)
}

// Role returns the most privileged client role a certificate's Organization grants; ok is
// false when it grants none.
func Role(cert *x509.Certificate) (role string, ok bool) {
	for _, r := range []string{RoleAdmin, RoleOperator, RoleReader} {
		if slices.Contains(cert.Subject.Organization, r) {
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
func ClientRole(chains [][]*x509.Certificate) (role string, ok bool) {
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
	return Role(chains[0][0])
}

// Allows reports whether a client with role may call something that requires the role need. The
// node role is apart from the others: it allows only what needs it, and only it allows that. An
// unrecognised need fails closed: Allows reports false rather than letting every role through.
func Allows(role, need string) bool {
	if role == RoleNode || need == RoleNode {
		return role == need && role != ""
	}
	rank := map[string]int{RoleReader: 1, RoleOperator: 2, RoleAdmin: 3}
	r, ok := rank[need]
	return ok && rank[role] >= r
}
