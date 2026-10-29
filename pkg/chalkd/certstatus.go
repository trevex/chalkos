package chalkd

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/pki"
)

// osCAs returns the OS CAs the node trusts.
func (s *Server) osCAs() string {
	if s.Certificate != nil {
		return s.Certificate.OSCA()
	}
	return string(s.readOSCA())
}

// trust lists what the node trusts and issues with, by fingerprint.
func (s *Server) trust() []*nodev1.TrustStatus {
	var list []*nodev1.TrustStatus
	if fps, err := pki.Fingerprints(s.osCAs()); err == nil {
		list = append(list, &nodev1.TrustStatus{Name: client.NameOSCA, Fingerprints: fps})
	}
	return list
}

// certificates lists every certificate the node holds or issues with what needs doing about it.
func (s *Server) certificates(now time.Time) []*nodev1.CertificateStatus {
	var list []*nodev1.CertificateStatus
	add := func(name string, cert, issuer *x509.Certificate, problem string) {
		list = append(list, layer.CertificateStatus(name, cert, issuer, problem))
	}
	if s.Certificate != nil {
		leaf := s.Certificate.Current().Leaf
		problem := layer.LeafProblem(leaf, now)
		switch {
		case s.Renewal != nil && s.Renewal.Problem() != "":
			problem = s.Renewal.Problem()
		case problem != "" && s.Renewal == nil:
			// Only nodes with Kubernetes reach a control plane to renew it.
			problem += "; renew it with chalkctl node renew " + s.nodeName()
		}
		var issuer *x509.Certificate
		if chain := s.Certificate.Current().TLS.Certificate; len(chain) > 1 {
			issuer, _ = x509.ParseCertificate(chain[1])
		}
		add(client.NameNode, leaf, issuer, problem)
	}
	if osCAs, err := pki.ParseBundle(s.osCAs()); err == nil {
		for _, osCA := range osCAs {
			problem := layer.CAProblem(osCA, now, layer.CAWarning)
			if problem != "" {
				problem = client.NameOSCA + " " + problem
			}
			add(client.NameOSCA, osCA, osCA, problem)
		}
	}
	return list
}

func (s *Server) readOSCA() []byte {
	data, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "chalkd", CAFile))
	return data
}
