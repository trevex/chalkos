package kubernetes

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
)

func TestCertificateProblems(t *testing.T) {
	now := time.Now()
	cert := func(from, until time.Duration) *x509.Certificate {
		return &x509.Certificate{NotBefore: now.Add(from), NotAfter: now.Add(until)}
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"leaf early", layer.LeafProblem(cert(-time.Hour, 300*24*time.Hour), now), ""},
		{"leaf in its last third", layer.LeafProblem(cert(-250*24*time.Hour, 100*24*time.Hour), now), "less than a third of its lifetime remains"},
		{"leaf expired", layer.LeafProblem(cert(-400*24*time.Hour, -time.Hour), now), "expired"},
		{"CA with years left", layer.CAProblem(cert(0, 2*layer.CAWarning), now, layer.CAWarning), ""},
		{"CA within its warning", layer.CAProblem(cert(0, layer.CAWarning-time.Hour), now, layer.CAWarning), "expires " + now.Add(layer.CAWarning-time.Hour).UTC().Format(time.DateOnly)},
		{"node CA within 18 months", layer.CAProblem(cert(0, 500*24*time.Hour), now, nodeCAWarning), "expires " + now.Add(500*24*time.Hour).UTC().Format(time.DateOnly)},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
