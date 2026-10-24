package clustersecrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// EncryptionKeySize is the size of the key the API server encrypts secrets at rest with
// (secretbox).
const EncryptionKeySize = 32

// KubernetesSecrets are the secrets of the cluster's Kubernetes control plane.
type KubernetesSecrets struct {
	// CA issues the API server's, the kubelets' and the clients' certificates.
	CA pki.CertKey `json:"ca"`
	// FrontProxyCA issues the certificate the API server presents to aggregated API servers.
	FrontProxyCA pki.CertKey `json:"frontProxyCA"`
	// EtcdCA issues etcd's certificates and those of its clients.
	EtcdCA pki.CertKey `json:"etcdCA"`
	// ServiceAccountKey signs service account tokens (ES256), PEM PKCS#8.
	ServiceAccountKey string `json:"serviceAccountKey"`
	// EncryptionKey encrypts secrets in etcd, under the name EncryptionKeyName, which ciphertexts
	// carry; empty names the key DefaultEncryptionKeyName.
	EncryptionKey     []byte `json:"encryptionKey"`
	EncryptionKeyName string `json:"encryptionKeyName,omitempty"`
	// Accepted holds what the components still trust besides the values above while one of them
	// rotates.
	Accepted KubernetesAccepted `json:"accepted,omitzero"`
}

// KubernetesPublic holds the certificates of the Kubernetes CAs.
type KubernetesPublic struct {
	CA           pki.CertKey `json:"ca"`
	FrontProxyCA pki.CertKey `json:"frontProxyCA"`
	EtcdCA       pki.CertKey `json:"etcdCA"`
}

// NewKubernetesSecrets creates the Kubernetes CAs, the service account key and the encryption
// key.
func NewKubernetesSecrets(now time.Time) (*KubernetesSecrets, error) {
	k := &KubernetesSecrets{}
	var err error
	if k.CA, err = pki.NewCA("chalkos Kubernetes CA", now); err != nil {
		return nil, err
	}
	if k.FrontProxyCA, err = pki.NewCA("chalkos front-proxy CA", now); err != nil {
		return nil, err
	}
	if k.EtcdCA, err = pki.NewCA("chalkos etcd CA", now); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	k.ServiceAccountKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	k.EncryptionKeyName = DefaultEncryptionKeyName
	k.EncryptionKey = make([]byte, EncryptionKeySize)
	if _, err := rand.Read(k.EncryptionKey); err != nil {
		return nil, err
	}
	return k, nil
}

// Public returns the CA certificates.
func (k *KubernetesSecrets) Public() *KubernetesPublic {
	return &KubernetesPublic{
		CA:           pki.CertKey{Certificate: k.CA.Certificate},
		FrontProxyCA: pki.CertKey{Certificate: k.FrontProxyCA.Certificate},
		EtcdCA:       pki.CertKey{Certificate: k.EtcdCA.Certificate},
	}
}

// String names the CAs and never prints a key, so logging the secrets does not leak them.
func (k KubernetesSecrets) String() string {
	return fmt.Sprintf("clustersecrets.KubernetesSecrets{ca: %v, frontProxyCA: %v, etcdCA: %v, serviceAccountKey: redacted, encryptionKey: redacted}",
		k.CA, k.FrontProxyCA, k.EtcdCA)
}

// GoString redacts the secrets like String.
func (k KubernetesSecrets) GoString() string {
	return k.String()
}

// cas names the Kubernetes CAs as the secrets file does.
func (k *KubernetesSecrets) cas() []pki.NamedCA {
	return []pki.NamedCA{{Name: "kubernetes.ca", CA: k.CA}, {Name: "kubernetes.frontProxyCA", CA: k.FrontProxyCA}, {Name: "kubernetes.etcdCA", CA: k.EtcdCA}}
}

// Validate checks that every CA is a CA certificate with its key and a key of its own, and the
// sizes of the keys.
func (k *KubernetesSecrets) Validate() error {
	cas := k.cas()
	for _, ca := range cas {
		if err := pki.ValidateCA(ca.CA); err != nil {
			return fmt.Errorf("%s: %w", ca.Name, err)
		}
	}
	if err := pki.RequireDistinctCAs(cas...); err != nil {
		return err
	}
	if _, err := pki.ParseECKey(k.ServiceAccountKey); err != nil {
		return fmt.Errorf("kubernetes.serviceAccountKey: %w", err)
	}
	if len(k.EncryptionKey) != EncryptionKeySize {
		return fmt.Errorf("kubernetes.encryptionKey must be %d bytes", EncryptionKeySize)
	}
	return nil
}
