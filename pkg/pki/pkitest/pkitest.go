// Package pkitest issues the cluster's CAs for tests.
package pkitest

import (
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// NodeCA returns a new node CA, valid from now, issued by a new OS CA.
func NodeCA(t testing.TB, now time.Time) pki.CertKey {
	t.Helper()
	osCA, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(osCA, now)
	if err != nil {
		t.Fatal(err)
	}
	return nodeCA
}
