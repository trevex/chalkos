package clustersecrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// EncryptionKeySize is the size of the key the API server encrypts secrets at rest with
// (secretbox).
const EncryptionKeySize = 32

// DefaultEncryptionKeyName is the name of a new secrets file's encryption key.
const DefaultEncryptionKeyName = "chalkos"

// KubernetesSecrets are the secrets of the cluster's Kubernetes control plane.
type KubernetesSecrets struct {
	// CA issues the API server's certificate, the kubelets' client certificates and the clients'
	// certificates.
	CA pki.CertKey `json:"ca"`
	// FrontProxyCA issues the certificate the API server presents to aggregated API servers.
	FrontProxyCA pki.CertKey `json:"frontProxyCA"`
	// EtcdCA issues etcd's certificates and those of its clients.
	EtcdCA pki.CertKey `json:"etcdCA"`
	// KubeletServingCA issues the kubelets' serving certificates, which the API server alone
	// trusts.
	KubeletServingCA pki.CertKey `json:"kubeletServingCA"`
	// ServiceAccountKey signs service account tokens (ES256), PEM PKCS#8.
	ServiceAccountKey string `json:"serviceAccountKey"`
	// EncryptionKey encrypts secrets in etcd, under the name EncryptionKeyName, which ciphertexts
	// carry.
	EncryptionKey     []byte `json:"encryptionKey"`
	EncryptionKeyName string `json:"encryptionKeyName"`
	// Accepted holds what the components still trust besides the values above while one of them
	// rotates.
	Accepted KubernetesAccepted `json:"accepted,omitzero"`
}

// KubernetesPublic holds the certificates of the Kubernetes CAs.
type KubernetesPublic struct {
	CA               pki.CertKey `json:"ca"`
	FrontProxyCA     pki.CertKey `json:"frontProxyCA"`
	EtcdCA           pki.CertKey `json:"etcdCA"`
	KubeletServingCA pki.CertKey `json:"kubeletServingCA"`
}

// NewKubernetesSecrets creates the Kubernetes CAs, the service account key and the encryption
// key.
func NewKubernetesSecrets(now time.Time) (*KubernetesSecrets, error) {
	k := &KubernetesSecrets{}
	var err error
	for _, slot := range k.caSlots() {
		if *slot.ca, err = pki.NewCA(slot.commonName, now); err != nil {
			return nil, err
		}
	}
	if k.ServiceAccountKey, err = newServiceAccountKey(); err != nil {
		return nil, err
	}
	k.EncryptionKeyName = DefaultEncryptionKeyName
	if k.EncryptionKey, err = newEncryptionKey(); err != nil {
		return nil, err
	}
	return k, nil
}

// newServiceAccountKey creates a P-256 service-account key, PEM PKCS#8.
func newServiceAccountKey() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	return pki.EncodeKey(key)
}

// newEncryptionKey creates a random encryption key.
func newEncryptionKey() ([]byte, error) {
	key := make([]byte, EncryptionKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// Public returns the CA certificates.
func (k *KubernetesSecrets) Public() *KubernetesPublic {
	return &KubernetesPublic{
		CA:               pki.CertKey{Certificate: k.CA.Certificate},
		FrontProxyCA:     pki.CertKey{Certificate: k.FrontProxyCA.Certificate},
		EtcdCA:           pki.CertKey{Certificate: k.EtcdCA.Certificate},
		KubeletServingCA: pki.CertKey{Certificate: k.KubeletServingCA.Certificate},
	}
}

// String names the CAs and never prints a key, so logging the secrets does not leak them.
func (k KubernetesSecrets) String() string {
	return fmt.Sprintf("clustersecrets.KubernetesSecrets{ca: %v, frontProxyCA: %v, etcdCA: %v, kubeletServingCA: %v, serviceAccountKey: redacted, encryptionKey: redacted}",
		k.CA, k.FrontProxyCA, k.EtcdCA, k.KubeletServingCA)
}

// GoString redacts the secrets like String.
func (k KubernetesSecrets) GoString() string {
	return k.String()
}

// caSlot is one Kubernetes CA: the field the secrets file holds it in, the common name it is
// created with, and where the CA, the certificates accepted besides it and its replacement during
// a rotation are kept.
type caSlot struct {
	field      string
	commonName string
	ca         *pki.CertKey
	accepted   *[]string
	new        func(*NewValues) **pki.CertKey
}

// caSlots lists the Kubernetes CAs in the order the secrets file holds them.
func (k *KubernetesSecrets) caSlots() []caSlot {
	return []caSlot{
		{"ca", "chalkos Kubernetes CA", &k.CA, &k.Accepted.CA, func(n *NewValues) **pki.CertKey { return &n.CA }},
		{"frontProxyCA", "chalkos front-proxy CA", &k.FrontProxyCA, &k.Accepted.FrontProxyCA, func(n *NewValues) **pki.CertKey { return &n.FrontProxyCA }},
		{"etcdCA", "chalkos etcd CA", &k.EtcdCA, &k.Accepted.EtcdCA, func(n *NewValues) **pki.CertKey { return &n.EtcdCA }},
		{"kubeletServingCA", "chalkos kubelet serving CA", &k.KubeletServingCA, &k.Accepted.KubeletServingCA, func(n *NewValues) **pki.CertKey { return &n.KubeletServingCA }},
	}
}

// cas names the Kubernetes CAs as the secrets file does.
func (k *KubernetesSecrets) cas() []pki.NamedCA {
	var cas []pki.NamedCA
	for _, slot := range k.caSlots() {
		cas = append(cas, pki.NamedCA{Name: "kubernetes." + slot.field, CA: *slot.ca})
	}
	return cas
}

// Validate checks that every CA is a CA certificate with its key and a key of its own, and the
// sizes of the keys.
func (k *KubernetesSecrets) Validate() error {
	cas := k.cas()
	for _, ca := range cas {
		if ca.CA == (pki.CertKey{}) {
			return fmt.Errorf("%s is missing, as in secrets from before chalkos had it; create the cluster's secrets again with chalkctl gen secrets", ca.Name)
		}
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
