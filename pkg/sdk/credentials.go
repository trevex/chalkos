package sdk

import (
	"crypto/tls"
	"fmt"
	"time"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
)

// AdminValidity is how long an admin certificate from AdminCertificate lives: it serves one run
// of a command or one reconcile and is never stored.
const AdminValidity = time.Hour

// AdminCertificate is an admin client certificate the OS CA of s issues to name at now: the
// caller's own name, which chalkd's logs and errors carry, such as "chalkctl" or a Cluster API
// provider's.
func AdminCertificate(s clustersecrets.Secrets, name string, now time.Time) (*tls.Certificate, error) {
	ck, err := pki.IssueClient(s.OSCA, name, pki.RoleAdmin, AdminValidity, now)
	if err != nil {
		return nil, fmt.Errorf("issue an admin certificate: %w", err)
	}
	cert, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		return nil, fmt.Errorf("load the admin certificate: %w", err)
	}
	return &cert, nil
}
