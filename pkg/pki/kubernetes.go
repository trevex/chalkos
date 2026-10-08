package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// EncryptionKeySize is the size of the key the API server encrypts secrets at rest with
// (secretbox).
const EncryptionKeySize = 32

// KubernetesSecrets are the secrets of the cluster's Kubernetes control plane.
type KubernetesSecrets struct {
	// CA issues the API server's, the kubelets' and the clients' certificates.
	CA CertKey `json:"ca"`
	// FrontProxyCA issues the certificate the API server presents to aggregated API servers.
	FrontProxyCA CertKey `json:"frontProxyCA"`
	// EtcdCA issues etcd's certificates and those of its clients.
	EtcdCA CertKey `json:"etcdCA"`
	// ServiceAccountKey signs service account tokens (ES256), PEM PKCS#8.
	ServiceAccountKey string `json:"serviceAccountKey"`
	// EncryptionKey encrypts secrets in etcd.
	EncryptionKey []byte `json:"encryptionKey"`
}

// KubernetesPublic holds the certificates of the Kubernetes CAs.
type KubernetesPublic struct {
	CA           CertKey `json:"ca"`
	FrontProxyCA CertKey `json:"frontProxyCA"`
	EtcdCA       CertKey `json:"etcdCA"`
}

// NewKubernetesSecrets creates the Kubernetes CAs, the service account key and the encryption
// key.
func NewKubernetesSecrets(now time.Time) (*KubernetesSecrets, error) {
	k := &KubernetesSecrets{}
	var err error
	if k.CA, err = NewCA("chalkos Kubernetes CA", now); err != nil {
		return nil, err
	}
	if k.FrontProxyCA, err = NewCA("chalkos front-proxy CA", now); err != nil {
		return nil, err
	}
	if k.EtcdCA, err = NewCA("chalkos etcd CA", now); err != nil {
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
	k.EncryptionKey = make([]byte, EncryptionKeySize)
	if _, err := rand.Read(k.EncryptionKey); err != nil {
		return nil, err
	}
	return k, nil
}

// Public returns the CA certificates.
func (k *KubernetesSecrets) Public() *KubernetesPublic {
	return &KubernetesPublic{
		CA:           CertKey{Certificate: k.CA.Certificate},
		FrontProxyCA: CertKey{Certificate: k.FrontProxyCA.Certificate},
		EtcdCA:       CertKey{Certificate: k.EtcdCA.Certificate},
	}
}

// String names the CAs and never prints a key, so logging the secrets does not leak them.
func (k KubernetesSecrets) String() string {
	return fmt.Sprintf("pki.KubernetesSecrets{ca: %v, frontProxyCA: %v, etcdCA: %v, serviceAccountKey: redacted, encryptionKey: redacted}",
		k.CA, k.FrontProxyCA, k.EtcdCA)
}

// GoString redacts the secrets like String.
func (k KubernetesSecrets) GoString() string {
	return k.String()
}

// cas names the Kubernetes CAs as the secrets file does.
func (k *KubernetesSecrets) cas() []NamedCA {
	return []NamedCA{{"kubernetes.ca", k.CA}, {"kubernetes.frontProxyCA", k.FrontProxyCA}, {"kubernetes.etcdCA", k.EtcdCA}}
}

// Validate checks that every CA is a CA certificate with its key and a key of its own, and the
// sizes of the keys.
func (k *KubernetesSecrets) Validate() error {
	cas := k.cas()
	for _, ca := range cas {
		if err := ValidateCA(ca.CA); err != nil {
			return fmt.Errorf("%s: %w", ca.Name, err)
		}
	}
	if err := RequireDistinctCAs(cas...); err != nil {
		return err
	}
	if _, err := ParseECKey(k.ServiceAccountKey); err != nil {
		return fmt.Errorf("kubernetes.serviceAccountKey: %w", err)
	}
	if len(k.EncryptionKey) != EncryptionKeySize {
		return fmt.Errorf("kubernetes.encryptionKey must be %d bytes", EncryptionKeySize)
	}
	return nil
}

// ValidateCA checks that a certificate and key form a CA that may sign certificates.
func ValidateCA(ca CertKey) error {
	cert, _, err := ca.Parse()
	if err != nil {
		return err
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return errors.New("not a CA certificate")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("the CA certificate lacks the certificate signing key usage")
	}
	return nil
}

// NamedCA is a CA with the name errors call it by.
type NamedCA struct {
	Name string
	CA   CertKey
}

// RequireDistinctCAs checks that no two CAs share a public key. The CAs grant different
// access: an etcd CA that is also the Kubernetes CA would give every certificate the Kubernetes
// CA issues full access to etcd.
func RequireDistinctCAs(cas ...NamedCA) error {
	keys := make([][]byte, len(cas))
	for i, ca := range cas {
		cert, err := ParseCertificate([]byte(ca.CA.Certificate))
		if err != nil {
			return fmt.Errorf("%s: %w", ca.Name, err)
		}
		keys[i] = cert.RawSubjectPublicKeyInfo
		for j := range i {
			if bytes.Equal(keys[j], keys[i]) {
				return fmt.Errorf("%s and %s share a key; every CA needs its own", cas[j].Name, ca.Name)
			}
		}
	}
	return nil
}

// ParseECKey decodes a PEM PKCS#8 ECDSA P-256 private key.
func ParseECKey(data string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(data))
	if block == nil {
		return nil, errors.New("no PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("the private key is not ECDSA P-256")
	}
	return key, nil
}
