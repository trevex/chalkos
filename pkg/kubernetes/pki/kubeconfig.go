package pki

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// Kubeconfig is a kubeconfig file with one cluster, one user and one context. Credentials are
// embedded, or named by path when the File fields are set.
type Kubeconfig struct {
	// Name names the cluster, the user and the context.
	Name   string
	Server string
	// ServerName is the name the server's certificate is verified for, when the server is
	// reached at another address.
	ServerName string
	// CA is the PEM certificate of the cluster's CA; CAFile names a file holding it instead.
	CA     []byte
	CAFile string
	// Client is the user's PEM certificate and key; ClientFile names a file holding both.
	Client     pki.CertKey
	ClientFile string
}

// Encode returns the kubeconfig as JSON, which kubectl and the components read like YAML.
func (k Kubeconfig) Encode() ([]byte, error) {
	if k.Name == "" || k.Server == "" {
		return nil, errors.New("a kubeconfig needs a name and a server")
	}
	cluster := map[string]any{"server": k.Server}
	if k.ServerName != "" {
		cluster["tls-server-name"] = k.ServerName
	}
	switch {
	case k.CAFile != "":
		cluster["certificate-authority"] = k.CAFile
	case len(k.CA) > 0:
		cluster["certificate-authority-data"] = k.CA
	default:
		return nil, errors.New("a kubeconfig needs the cluster's CA")
	}
	user := map[string]any{}
	switch {
	case k.ClientFile != "":
		user["client-certificate"] = k.ClientFile
		user["client-key"] = k.ClientFile
	case k.Client.Certificate != "" && k.Client.Key != "":
		user["client-certificate-data"] = []byte(k.Client.Certificate)
		user["client-key-data"] = []byte(k.Client.Key)
	default:
		return nil, errors.New("a kubeconfig needs a client certificate and key")
	}
	data, err := json.MarshalIndent(map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"clusters":        []any{map[string]any{"name": k.Name, "cluster": cluster}},
		"users":           []any{map[string]any{"name": k.Name, "user": user}},
		"contexts":        []any{map[string]any{"name": k.Name, "context": map[string]any{"cluster": k.Name, "user": k.Name}}},
		"current-context": k.Name,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// IssueAdmin issues a client certificate in the group bound to cluster-admin by the built-in
// manifests. RBAC can revoke the binding, which it cannot for system:masters.
func IssueAdmin(ca pki.CertKey, name string, ttl time.Duration, now time.Time) (pki.CertKey, error) {
	if ttl <= 0 {
		return pki.CertKey{}, errors.New("the validity must be positive")
	}
	return pki.IssueLeaf(ca, pki.Leaf{CommonName: name, Organization: []string{ClusterAdminGroup}, Client: true, Validity: ttl}, now)
}
