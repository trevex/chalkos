package apply

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	nodeUserPrefix = "system:node:"
	nodesGroup     = "system:nodes"
)

// Approvable returns nil when a kubelet serving certificate request may be approved: a node
// asks for itself, in its own name, for addresses its Node object lists, and for server use
// only. Otherwise it says why not.
func Approvable(csr *certificatesv1.CertificateSigningRequest, node *corev1.Node) error {
	if csr.Spec.SignerName != certificatesv1.KubeletServingSignerName {
		return fmt.Errorf("signer %s is not the kubelet serving signer", csr.Spec.SignerName)
	}
	name, ok := strings.CutPrefix(csr.Spec.Username, nodeUserPrefix)
	if !ok || name == "" || !slices.Contains(csr.Spec.Groups, nodesGroup) {
		return fmt.Errorf("requested by %s, which is not a node", csr.Spec.Username)
	}
	if node == nil || node.Name != name {
		return fmt.Errorf("node %s has no Node object", name)
	}
	block, _ := pem.Decode(csr.Spec.Request)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return errors.New("the request holds no PEM certificate request")
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse the certificate request: %w", err)
	}
	if req.Subject.CommonName != csr.Spec.Username || !slices.Equal(req.Subject.Organization, []string{nodesGroup}) {
		return fmt.Errorf("the request's subject %s is not the requesting node's", req.Subject)
	}
	if len(req.EmailAddresses) > 0 || len(req.URIs) > 0 {
		return errors.New("the request names email addresses or URIs")
	}
	if len(req.DNSNames)+len(req.IPAddresses) == 0 {
		return errors.New("the request names no address")
	}
	allowed := map[certificatesv1.KeyUsage]bool{
		certificatesv1.UsageDigitalSignature: true,
		certificatesv1.UsageKeyEncipherment:  true,
		certificatesv1.UsageServerAuth:       true,
	}
	for _, u := range csr.Spec.Usages {
		if !allowed[u] {
			return fmt.Errorf("the request asks for %s", u)
		}
	}
	if !slices.Contains(csr.Spec.Usages, certificatesv1.UsageServerAuth) {
		return errors.New("the request does not ask for server authentication")
	}
	var ips, names []string
	for _, a := range node.Status.Addresses {
		switch a.Type {
		case corev1.NodeInternalIP, corev1.NodeExternalIP:
			ips = append(ips, a.Address)
		case corev1.NodeHostName, corev1.NodeInternalDNS, corev1.NodeExternalDNS:
			names = append(names, a.Address)
		}
	}
	for _, ip := range req.IPAddresses {
		if !slices.Contains(ips, ip.String()) {
			return fmt.Errorf("node %s does not report the address %s", name, ip)
		}
	}
	for _, n := range req.DNSNames {
		if !slices.Contains(names, n) {
			return fmt.Errorf("node %s does not report the name %s", name, n)
		}
	}
	return nil
}

// Approver approves kubelet serving certificate requests that Approvable accepts and leaves
// the others pending.
type Approver struct {
	Client kubernetes.Interface
	// refused remembers the requests already logged as refused.
	refused map[string]bool
}

// Run approves requests every interval until ctx ends.
func (a *Approver) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := a.Once(ctx); err != nil && ctx.Err() == nil {
			log.Printf("approve kubelet serving certificates: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Once looks at the pending requests once. A request that fails does not stop the others;
// the error names every one that failed.
func (a *Approver) Once(ctx context.Context) error {
	if a.refused == nil {
		a.refused = map[string]bool{}
	}
	list, err := a.Client.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	var errs []error
	for i := range list.Items {
		csr := &list.Items[i]
		exists[csr.Name] = true
		if err := a.review(ctx, csr); err != nil {
			errs = append(errs, fmt.Errorf("certificate request %s: %w", csr.Name, err))
		}
	}
	for name := range a.refused {
		if !exists[name] {
			delete(a.refused, name)
		}
	}
	return errors.Join(errs...)
}

// review approves one request if Approvable accepts it.
func (a *Approver) review(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) error {
	if csr.Spec.SignerName != certificatesv1.KubeletServingSignerName || decided(csr) {
		return nil
	}
	var node *corev1.Node
	if name, ok := strings.CutPrefix(csr.Spec.Username, nodeUserPrefix); ok && name != "" {
		n, err := a.Client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			node = n
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("read node %s: %w", name, err)
		}
	}
	if err := Approvable(csr, node); err != nil {
		if !a.refused[csr.Name] {
			log.Printf("leaving certificate request %s pending: %v", csr.Name, err)
			a.refused[csr.Name] = true
		}
		return nil
	}
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         corev1.ConditionTrue,
		Reason:         "ChalkdApproved",
		Message:        "The node's name and addresses match its Node object.",
		LastUpdateTime: metav1.Now(),
	})
	if _, err := a.Client.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	log.Printf("approved certificate request %s of %s", csr.Name, csr.Spec.Username)
	return nil
}

func decided(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, c := range csr.Status.Conditions {
		if c.Type == certificatesv1.CertificateApproved || c.Type == certificatesv1.CertificateDenied || c.Type == certificatesv1.CertificateFailed {
			return true
		}
	}
	return false
}
