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

// A kubeconfig file as Encode writes it and kubeconfigCertificate reads it, its fields in key
// order.
type (
	kubeconfigFile struct {
		APIVersion     string         `json:"apiVersion"`
		Clusters       []namedCluster `json:"clusters"`
		Contexts       []namedContext `json:"contexts"`
		CurrentContext string         `json:"current-context"`
		Kind           string         `json:"kind"`
		Users          []namedUser    `json:"users"`
	}
	namedCluster struct {
		Cluster kubeconfigCluster `json:"cluster"`
		Name    string            `json:"name"`
	}
	kubeconfigCluster struct {
		CertificateAuthority     string `json:"certificate-authority,omitempty"`
		CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`
		Server                   string `json:"server"`
		TLSServerName            string `json:"tls-server-name,omitempty"`
	}
	namedContext struct {
		Context kubeconfigContext `json:"context"`
		Name    string            `json:"name"`
	}
	kubeconfigContext struct {
		Cluster string `json:"cluster"`
		User    string `json:"user"`
	}
	namedUser struct {
		Name string         `json:"name"`
		User kubeconfigUser `json:"user"`
	}
	kubeconfigUser struct {
		ClientCertificate     string `json:"client-certificate,omitempty"`
		ClientCertificateData []byte `json:"client-certificate-data,omitempty"`
		ClientKey             string `json:"client-key,omitempty"`
		ClientKeyData         []byte `json:"client-key-data,omitempty"`
	}
)

// Encode returns the kubeconfig as JSON, which kubectl and the components read like YAML.
func (k Kubeconfig) Encode() ([]byte, error) {
	if k.Name == "" || k.Server == "" {
		return nil, errors.New("a kubeconfig needs a name and a server")
	}
	cluster := kubeconfigCluster{Server: k.Server, TLSServerName: k.ServerName}
	switch {
	case k.CAFile != "":
		cluster.CertificateAuthority = k.CAFile
	case len(k.CA) > 0:
		cluster.CertificateAuthorityData = k.CA
	default:
		return nil, errors.New("a kubeconfig needs the cluster's CA")
	}
	var user kubeconfigUser
	switch {
	case k.ClientFile != "":
		user.ClientCertificate = k.ClientFile
		user.ClientKey = k.ClientFile
	case k.Client.Certificate != "" && k.Client.Key != "":
		user.ClientCertificateData = []byte(k.Client.Certificate)
		user.ClientKeyData = []byte(k.Client.Key)
	default:
		return nil, errors.New("a kubeconfig needs a client certificate and key")
	}
	data, err := json.MarshalIndent(kubeconfigFile{
		APIVersion:     "v1",
		Kind:           "Config",
		Clusters:       []namedCluster{{Name: k.Name, Cluster: cluster}},
		Users:          []namedUser{{Name: k.Name, User: user}},
		Contexts:       []namedContext{{Name: k.Name, Context: kubeconfigContext{Cluster: k.Name, User: k.Name}}},
		CurrentContext: k.Name,
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
