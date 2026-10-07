package pki

import (
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

// Validate checks that every CA is a CA certificate with its key, and the sizes of the keys.
func (k *KubernetesSecrets) Validate() error {
	for _, ca := range []struct {
		name string
		ck   CertKey
	}{{"ca", k.CA}, {"frontProxyCA", k.FrontProxyCA}, {"etcdCA", k.EtcdCA}} {
		if err := ValidateCA(ca.ck); err != nil {
			return fmt.Errorf("kubernetes.%s: %w", ca.name, err)
		}
	}
	if _, err := ParseECKey(k.ServiceAccountKey); err != nil {
		return fmt.Errorf("kubernetes.serviceAccountKey: %w", err)
	}
	if len(k.EncryptionKey) != EncryptionKeySize {
		return fmt.Errorf("kubernetes.encryptionKey must be %d bytes", EncryptionKeySize)
	}
	return nil
}

// ValidateCA checks that a certificate and key form a CA.
func ValidateCA(ca CertKey) error {
	cert, _, err := ca.Parse()
	if err != nil {
		return err
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return errors.New("not a CA certificate")
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

// errNoKubernetes names the command that adds the Kubernetes secrets to a version 1 file.
var errNoKubernetes = errors.New("the secrets file has no Kubernetes secrets (version 1); add them with chalkctl secrets upgrade --out FILE")

// RequireKubernetes returns the Kubernetes secrets, or an error naming chalkctl secrets
// upgrade when the file predates them.
func (s Secrets) RequireKubernetes() (*KubernetesSecrets, error) {
	if s.Kubernetes == nil {
		return nil, errNoKubernetes
	}
	return s.Kubernetes, nil
}

// Upgrade adds the Kubernetes secrets to a version 1 secrets file and keeps everything else.
func Upgrade(s Secrets, now time.Time) (Secrets, error) {
	if s.Version != 1 {
		return Secrets{}, fmt.Errorf("the secrets file is version %d; only version 1 is upgraded", s.Version)
	}
	k, err := NewKubernetesSecrets(now)
	if err != nil {
		return Secrets{}, err
	}
	s.Version = SecretsVersion
	s.Kubernetes = k
	return s, nil
}
