// Package pki issues the certificates of a chalkos Kubernetes cluster: the share each node
// receives at install, the control plane's certificates and kubeconfigs, and admin
// kubeconfigs.
package pki

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

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
	// NodeCA is the node CA with its key, with which a control-plane node renews node
	// certificates. Workers do not hold it.
	NodeCA *pki.CertKey `json:"nodeCA,omitempty"`
}

// ControlPlaneShare is what a control-plane node receives: every Kubernetes CA with its key, the
// keys, and the node CA.
func ControlPlaneShare(k *pki.KubernetesSecrets, nodeCA pki.CertKey) Share {
	front, etcd := k.FrontProxyCA, k.EtcdCA
	return Share{
		Kind:              kubernetes.KindControlPlane,
		CA:                k.CA,
		FrontProxyCA:      &front,
		EtcdCA:            &etcd,
		ServiceAccountKey: k.ServiceAccountKey,
		EncryptionKey:     k.EncryptionKey,
		NodeCA:            &nodeCA,
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

// ShareFor returns the share of a node of the kind from the secrets file.
func ShareFor(s *pki.Secrets, kind, node string, now time.Time) (Share, error) {
	switch kind {
	case kubernetes.KindControlPlane:
		return ControlPlaneShare(&s.Kubernetes, s.NodeCA), nil
	case kubernetes.KindWorker:
		return WorkerShare(&s.Kubernetes, node, now)
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

// ParseShare decodes and validates a share. It refuses unknown fields and data after the share:
// a share is only ever what Encode wrote.
func ParseShare(data []byte) (Share, error) {
	var s Share
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Share{}, fmt.Errorf("parse the Kubernetes share: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Share{}, errors.New("parse the Kubernetes share: data after the share")
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
		if s.FrontProxyCA == nil || s.EtcdCA == nil || s.NodeCA == nil || s.Kubelet != nil {
			return errors.New("a control-plane share holds the front-proxy, etcd and node CAs and no kubelet certificate")
		}
		if nodeCA, err := pki.ParseCertificate([]byte(s.NodeCA.Certificate)); err != nil || !pki.IsNodeCA(nodeCA) {
			return errors.New("Kubernetes share: nodeCA is not a node CA")
		}
		cas := []pki.NamedCA{{Name: "ca", CA: s.CA}, {Name: "frontProxyCA", CA: *s.FrontProxyCA}, {Name: "etcdCA", CA: *s.EtcdCA}, {Name: "nodeCA", CA: *s.NodeCA}}
		for _, ca := range cas {
			if err := pki.ValidateCA(ca.CA); err != nil {
				return fmt.Errorf("Kubernetes share: %s: %w", ca.Name, err)
			}
		}
		if err := pki.RequireDistinctCAs(cas...); err != nil {
			return fmt.Errorf("Kubernetes share: %w", err)
		}
		if _, err := pki.ParseECKey(s.ServiceAccountKey); err != nil {
			return fmt.Errorf("Kubernetes share: service account key: %w", err)
		}
		if len(s.EncryptionKey) != pki.EncryptionKeySize {
			return fmt.Errorf("Kubernetes share: the encryption key must be %d bytes", pki.EncryptionKeySize)
		}
	case kubernetes.KindWorker:
		if s.CA.Key != "" || s.FrontProxyCA != nil || s.EtcdCA != nil || s.NodeCA != nil || s.ServiceAccountKey != "" || s.EncryptionKey != nil {
			return errors.New("a worker share holds no CA key, node CA, service account key or encryption key")
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
		// Any other group, system:masters above all, would give the kubelet more than the node
		// authorizer allows a node.
		if !slices.Equal(cert.Subject.Organization, []string{NodesGroup}) {
			return fmt.Errorf("Kubernetes share: the kubelet certificate's groups are %q, want only %q", cert.Subject.Organization, NodesGroup)
		}
		name, ok := strings.CutPrefix(cert.Subject.CommonName, NodeUserPrefix)
		if !ok || name == "" {
			return fmt.Errorf("Kubernetes share: the kubelet certificate is for %q, not a node", cert.Subject.CommonName)
		}
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return fmt.Errorf("Kubernetes share: the kubelet certificate's node name %q is invalid: %s", name, strings.Join(errs, "; "))
		}
	default:
		return fmt.Errorf("Kubernetes share: unknown kind %q", s.Kind)
	}
	return nil
}

// ValidateFor validates the share and checks that a worker's kubelet certificate is for
// nodeName, so a node never runs its kubelet as another node.
func (s Share) ValidateFor(nodeName string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Kind == kubernetes.KindWorker {
		if node := s.Node(); node != nodeName {
			return fmt.Errorf("Kubernetes share: the kubelet certificate is for node %q, not %q", node, nodeName)
		}
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
