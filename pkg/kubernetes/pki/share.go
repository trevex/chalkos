// Package pki issues the certificates of a chalkos Kubernetes cluster: the share each node
// receives at install, the control plane's certificates and kubeconfigs, and admin
// kubeconfigs.
package pki

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

// Groups and name prefixes Kubernetes gives meaning to.
const (
	NodesGroup        = "system:nodes"
	NodeUserPrefix    = "system:node:"
	ClusterAdminGroup = "chalkos:cluster-admins"
)

// Share is the part of the cluster's Kubernetes secrets one node receives.
type Share struct {
	// Kind is the node's kind; it decides what the share holds.
	Kind string `json:"kind"`
	// CA is the Kubernetes CA: with its key on control-plane nodes, the certificate alone on
	// workers.
	CA pki.CertKey `json:"ca"`
	// The rest of the control plane's secrets.
	FrontProxyCA      *pki.CertKey `json:"frontProxyCA,omitempty"`
	EtcdCA            *pki.CertKey `json:"etcdCA,omitempty"`
	ServiceAccountKey string       `json:"serviceAccountKey,omitempty"`
	EncryptionKey     []byte       `json:"encryptionKey,omitempty"`
	// Kubelet is a worker's kubelet client certificate, which the kubelet renews itself.
	Kubelet *pki.CertKey `json:"kubelet,omitempty"`
}

// ControlPlaneShare is what a control-plane node receives: every CA with its key and the keys.
func ControlPlaneShare(k *pki.KubernetesSecrets) Share {
	front, etcd := k.FrontProxyCA, k.EtcdCA
	return Share{
		Kind:              kubernetes.KindControlPlane,
		CA:                k.CA,
		FrontProxyCA:      &front,
		EtcdCA:            &etcd,
		ServiceAccountKey: k.ServiceAccountKey,
		EncryptionKey:     k.EncryptionKey,
	}
}

// WorkerShare is what a worker receives: the CA certificate and a kubelet client certificate
// for the node, issued now.
func WorkerShare(k *pki.KubernetesSecrets, node string, now time.Time) (Share, error) {
	kubelet, err := IssueKubeletClient(k.CA, node, now)
	if err != nil {
		return Share{}, err
	}
	return Share{Kind: kubernetes.KindWorker, CA: pki.CertKey{Certificate: k.CA.Certificate}, Kubelet: &kubelet}, nil
}

// ShareFor returns the share of a node of the kind.
func ShareFor(k *pki.KubernetesSecrets, kind, node string, now time.Time) (Share, error) {
	switch kind {
	case kubernetes.KindControlPlane:
		return ControlPlaneShare(k), nil
	case kubernetes.KindWorker:
		return WorkerShare(k, node, now)
	}
	return Share{}, fmt.Errorf("unknown Kubernetes kind %q", kind)
}

// IssueKubeletClient issues the client certificate a node's kubelet authenticates with.
func IssueKubeletClient(ca pki.CertKey, node string, now time.Time) (pki.CertKey, error) {
	if node == "" {
		return pki.CertKey{}, errors.New("no node name")
	}
	return pki.IssueLeaf(ca, pki.Leaf{CommonName: NodeUserPrefix + node, Organization: []string{NodesGroup}, Client: true}, now)
}

// ParseShare decodes and validates a share.
func ParseShare(data []byte) (Share, error) {
	var s Share
	if err := json.Unmarshal(data, &s); err != nil {
		return Share{}, fmt.Errorf("parse the Kubernetes share: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Share{}, err
	}
	return s, nil
}

// Validate checks that the share holds what its kind needs, and nothing a worker must not hold.
func (s Share) Validate() error {
	switch s.Kind {
	case kubernetes.KindControlPlane:
		if s.FrontProxyCA == nil || s.EtcdCA == nil || s.Kubelet != nil {
			return errors.New("a control-plane share holds the front-proxy and etcd CAs and no kubelet certificate")
		}
		for name, ca := range map[string]pki.CertKey{"ca": s.CA, "frontProxyCA": *s.FrontProxyCA, "etcdCA": *s.EtcdCA} {
			if err := pki.ValidateCA(ca); err != nil {
				return fmt.Errorf("Kubernetes share: %s: %w", name, err)
			}
		}
		if _, err := pki.ParseECKey(s.ServiceAccountKey); err != nil {
			return fmt.Errorf("Kubernetes share: service account key: %w", err)
		}
		if len(s.EncryptionKey) != pki.EncryptionKeySize {
			return fmt.Errorf("Kubernetes share: the encryption key must be %d bytes", pki.EncryptionKeySize)
		}
	case kubernetes.KindWorker:
		if s.CA.Key != "" || s.FrontProxyCA != nil || s.EtcdCA != nil || s.ServiceAccountKey != "" || s.EncryptionKey != nil {
			return errors.New("a worker share holds no CA key, service account key or encryption key")
		}
		ca, err := pki.ParseCertificate([]byte(s.CA.Certificate))
		if err != nil {
			return fmt.Errorf("Kubernetes share: ca: %w", err)
		}
		if s.Kubelet == nil {
			return errors.New("a worker share holds a kubelet client certificate")
		}
		cert, _, err := s.Kubelet.Parse()
		if err != nil {
			return fmt.Errorf("Kubernetes share: kubelet: %w", err)
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: cert.NotBefore}); err != nil {
			return fmt.Errorf("Kubernetes share: the kubelet certificate does not verify against the CA: %w", err)
		}
		if !strings.HasPrefix(cert.Subject.CommonName, NodeUserPrefix) {
			return fmt.Errorf("Kubernetes share: the kubelet certificate is for %q, not a node", cert.Subject.CommonName)
		}
	default:
		return fmt.Errorf("Kubernetes share: unknown kind %q", s.Kind)
	}
	return nil
}

// Node returns the node name a worker's kubelet certificate is for; empty on a control plane.
func (s Share) Node() string {
	if s.Kubelet == nil {
		return ""
	}
	cert, err := pki.ParseCertificate([]byte(s.Kubelet.Certificate))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(cert.Subject.CommonName, NodeUserPrefix)
}

// Encode returns the share as chalkd stores it.
func (s Share) Encode() ([]byte, error) {
	return json.Marshal(s)
}

// String redacts the share, so logging it or an error wrapping it never leaks a key.
func (s Share) String() string {
	return fmt.Sprintf("kubernetes.Share{kind: %s, redacted}", s.Kind)
}

// GoString redacts the share like String.
func (s Share) GoString() string {
	return s.String()
}
