package layer

import (
	"crypto/x509"
	"testing"
	"time"
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
		{"leaf early", LeafProblem(cert(-time.Hour, 300*24*time.Hour), now), ""},
		{"leaf in its last third", LeafProblem(cert(-250*24*time.Hour, 100*24*time.Hour), now), "less than a third of its lifetime remains"},
		{"leaf expired", LeafProblem(cert(-400*24*time.Hour, -time.Hour), now), "expired"},
		{"CA with years left", CAProblem(cert(0, 2*CAWarning), now, CAWarning), ""},
		{"CA within its warning", CAProblem(cert(0, CAWarning-time.Hour), now, CAWarning), "expires " + now.Add(CAWarning-time.Hour).UTC().Format(time.DateOnly)},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
