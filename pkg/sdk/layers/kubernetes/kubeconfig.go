package kubernetes

import (
	"fmt"
	"net/url"
	"time"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// KubeconfigRequest names what an admin kubeconfig holds.
type KubeconfigRequest struct {
	// Context is the name of the kubeconfig's cluster, user and context.
	Context string
	// Server is the cluster's endpoint.
	Server string
	// Via is another URL clients reach the API server at, when it differs from Server; the API
	// server's certificate is still verified for Server's host. Empty means Server.
	Via string
	// User is the user name in the admin certificate, whose group chalkos's manifests bind to
	// cluster-admin.
	User string
	// TTL is the validity of the admin certificate; it must be positive.
	TTL time.Duration
}

// Kubeconfig is an admin kubeconfig of the cluster of s, whose client certificate the Kubernetes
// CA issues at now, and which trusts the cluster's CA bundle.
func Kubeconfig(s clustersecrets.Secrets, k KubeconfigRequest, now time.Time) ([]byte, error) {
	admin, err := kpki.IssueAdmin(s.KubeconfigCA(), k.User, k.TTL, now)
	if err != nil {
		return nil, err
	}
	kc := kpki.Kubeconfig{Name: k.Context, Server: k.Server, CA: []byte(s.Kubernetes.CABundle()), Client: admin}
	if k.Via != "" {
		u, err := url.Parse(k.Server)
		if err != nil {
			return nil, fmt.Errorf("the cluster endpoint: %w", err)
		}
		kc.Server, kc.ServerName = k.Via, u.Hostname()
	}
	return kc.Encode()
}
