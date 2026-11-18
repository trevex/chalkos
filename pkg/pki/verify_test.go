package pki

import (
	"crypto/x509"
	"testing"
	"time"
)

// TestVerifyClient verifies a client of the OS CA, also through the node CA it presents, at the
// time given: not once it expired, nor against roots without its OS CA, nor without roots.
func TestVerifyClient(t *testing.T) {
	osCA, nodeCA := newTestNodeCA(t)
	roots, err := BundlePool(osCA.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	client, err := IssueClient(osCA, "c", RoleAdmin, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := IssueNode(nodeCA, NodeNames{CommonName: "n1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	presented := func(c CertKey) []*x509.Certificate {
		certs, err := ParseBundle(c.Certificate)
		if err != nil {
			t.Fatal(err)
		}
		return certs
	}
	if chains, err := VerifyClient(presented(client), roots, now); err != nil || len(chains[0]) != 2 {
		t.Errorf("the admin: %v, %v", chains, err)
	}
	if chains, err := VerifyClient(presented(node), roots, now); err != nil || len(chains[0]) != 3 {
		t.Errorf("the node: %v, %v", chains, err)
	}
	other, err := BundlePool(newTestCA(t).Certificate)
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"expired":        verifyErr(VerifyClient(presented(client), roots, now.Add(2*time.Hour))),
		"another OS CA":  verifyErr(VerifyClient(presented(client), other, now)),
		"no roots":       verifyErr(VerifyClient(presented(client), nil, now)),
		"no certificate": verifyErr(VerifyClient(nil, roots, now)),
	} {
		if err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func verifyErr(_ [][]*x509.Certificate, err error) error { return err }
