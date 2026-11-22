package apply

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

// Approvable returns nil when a kubelet serving certificate request may be approved: a node
// asks for itself, in its own name, for server use only, and for addresses of its own. Otherwise
// it says why not. An address of its own is one its Node object lists and no other Node in nodes
// lists, outside the cluster's pod and service ranges and none of its virtual addresses; a name
// of its own is the node's name or the host name its Node object lists, outside the cluster's
// domain, below no .svc and no kubernetes service name. The cluster's endpoint is refused as
// well, unless it is an address and the cluster has no virtual address: then it is a control
// plane's own address, which that node's certificate holds.
func Approvable(csr *certificatesv1.CertificateSigningRequest, node *corev1.Node, nodes []corev1.Node, c k8s.Cluster) error {
	if csr.Spec.SignerName != certificatesv1.KubeletServingSignerName {
		return fmt.Errorf("signer %s is not the kubelet serving signer", csr.Spec.SignerName)
	}
	name, ok := strings.CutPrefix(csr.Spec.Username, kpki.NodeUserPrefix)
	if !ok || name == "" || !slices.Contains(csr.Spec.Groups, kpki.NodesGroup) {
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
	if req.Subject.CommonName != csr.Spec.Username || !slices.Equal(req.Subject.Organization, []string{kpki.NodesGroup}) {
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
	ips, names := nodeAddresses(node)
	names = append(names, node.Name)
	// What other nodes list, and what stands for the cluster itself, no node may hold.
	taken := map[string]string{}
	for i := range nodes {
		if nodes[i].Name == name {
			continue
		}
		otherIPs, otherNames := nodeAddresses(&nodes[i])
		for _, a := range append(append(otherIPs, otherNames...), nodes[i].Name) {
			taken[a] = "node " + nodes[i].Name
		}
	}
	for _, ip := range c.VIPAddresses() {
		taken[ip.String()] = "the cluster's virtual addresses"
	}
	host := c.EndpointHost()
	if _, err := netip.ParseAddr(host); err != nil || len(c.VIP.Addresses) > 0 {
		taken[strings.ToLower(host)] = "the cluster's endpoint"
	}
	ranges := append(c.PodCIDRList(), c.ServiceCIDRList()...)
	for _, raw := range req.IPAddresses {
		ip, _ := netip.AddrFromSlice(raw)
		ip = ip.Unmap()
		if !slices.Contains(ips, ip.String()) {
			return fmt.Errorf("node %s does not report the address %s", name, ip)
		}
		if who, ok := taken[ip.String()]; ok {
			return fmt.Errorf("the address %s belongs to %s", ip, who)
		}
		for _, r := range ranges {
			if r.Contains(ip) {
				return fmt.Errorf("the address %s is in the cluster's range %s", ip, r)
			}
		}
	}
	for _, n := range req.DNSNames {
		if !slices.Contains(names, n) {
			return fmt.Errorf("the name %s is neither the name nor the host name of node %s", n, name)
		}
		if why := clusterName(strings.ToLower(n), c.Domain); why != "" {
			return fmt.Errorf("the name %s is %s", n, why)
		}
		if who, ok := taken[strings.ToLower(n)]; ok {
			return fmt.Errorf("the name %s belongs to %s", n, who)
		}
	}
	return nil
}

// nodeAddresses returns the addresses and the host names node's Node object lists.
func nodeAddresses(node *corev1.Node) (ips, names []string) {
	for _, a := range node.Status.Addresses {
		switch a.Type {
		case corev1.NodeInternalIP, corev1.NodeExternalIP:
			if ip, err := netip.ParseAddr(a.Address); err == nil {
				ips = append(ips, ip.Unmap().String())
			}
		case corev1.NodeHostName:
			names = append(names, a.Address)
		}
	}
	return ips, names
}

// clusterName says why the lower-case name n stands for the cluster or a service in it, or
// returns "" when it does not.
func clusterName(n, domain string) string {
	switch {
	case n == domain || strings.HasSuffix(n, "."+domain):
		return "in the cluster's domain " + domain
	case n == "svc" || strings.HasSuffix(n, ".svc") || strings.Contains(n, ".svc."):
		return "below .svc"
	case n == "kubernetes" || strings.HasPrefix(n, "kubernetes."):
		return "the kubernetes service's"
	}
	return ""
}

// Approver approves kubelet serving certificate requests that Approvable accepts and leaves
// the others pending, as it does those of revoked nodes. It also keeps the revoked nodes'
// admission policy in step with their ConfigMap.
type Approver struct {
	Client kubernetes.Interface
	// Cluster is the cluster whose ranges, virtual addresses, endpoint and domain no node's
	// serving certificate may hold.
	Cluster k8s.Cluster
	// Revoked returns the revoked nodes; nil reads them through Client. Tests replace it.
	Revoked func(context.Context) (revoked.List, error)
	// refused remembers the requests already logged as refused.
	refused map[string]bool
	// enforced is the resource version of the revoked nodes' ConfigMap the admission policy was
	// last applied from, "" without the ConfigMap; nil before the first apply.
	enforced *string
}

// Run approves requests and enforces the revoked nodes' admission policy every interval until
// ctx ends.
func (a *Approver) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := a.Once(ctx); err != nil && ctx.Err() == nil {
			log.Printf("approve kubelet serving certificates: %v", err)
		}
		if err := a.Enforce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("enforce the revoked nodes' admission policy: %v", err)
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
	// The revoked nodes are read once a pass, and only when a request awaits a decision.
	revokedNodes := sync.OnceValues(func() (revoked.List, error) {
		if a.Revoked != nil {
			return a.Revoked(ctx)
		}
		return revoked.Load(ctx, a.Client)
	})
	// So are the Nodes, whose addresses a request must not name unless they are its own node's.
	nodes := sync.OnceValues(func() ([]corev1.Node, error) {
		list, err := a.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
	exists := map[string]bool{}
	var errs []error
	// Requests left pending because the revoked nodes could not be read share one error.
	unread := 0
	for i := range list.Items {
		csr := &list.Items[i]
		exists[csr.Name] = true
		err := a.review(ctx, csr, revokedNodes, nodes)
		_, pending := errors.AsType[unreadRevoked](err)
		switch {
		case pending:
			unread++
		case err != nil:
			errs = append(errs, fmt.Errorf("certificate request %s: %w", csr.Name, err))
		}
	}
	if unread > 0 {
		_, err := revokedNodes()
		errs = append(errs, fmt.Errorf("left %d certificate requests pending: %w", unread, err))
	}
	for name := range a.refused {
		if !exists[name] {
			delete(a.refused, name)
		}
	}
	return errors.Join(errs...)
}

// review approves one request if Approvable accepts it and its node is not revoked. While the
// revoked nodes cannot be read, it leaves the request pending and returns unreadRevoked.
func (a *Approver) review(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, revokedNodes func() (revoked.List, error), nodes func() ([]corev1.Node, error)) error {
	if csr.Spec.SignerName != certificatesv1.KubeletServingSignerName || decided(csr) {
		return nil
	}
	var node *corev1.Node
	var all []corev1.Node
	if name, ok := strings.CutPrefix(csr.Spec.Username, kpki.NodeUserPrefix); ok && name != "" {
		list, err := revokedNodes()
		if err != nil {
			return unreadRevoked{err}
		}
		if at, ok := list[name]; ok {
			a.leavePending(csr, revokedError(name, at))
			return nil
		}
		n, err := a.Client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			node = n
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("read node %s: %w", name, err)
		}
		if all, err = nodes(); err != nil {
			return fmt.Errorf("list the nodes: %w", err)
		}
	}
	if err := Approvable(csr, node, all, a.Cluster); err != nil {
		a.leavePending(csr, err)
		return nil
	}
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         corev1.ConditionTrue,
		Reason:         "ChalkdApproved",
		Message:        "The node's name and addresses are its own.",
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

// leavePending logs, once for each request, why it stays pending.
func (a *Approver) leavePending(csr *certificatesv1.CertificateSigningRequest, why error) {
	if !a.refused[csr.Name] {
		log.Printf("leaving certificate request %s pending: %v", csr.Name, why)
		a.refused[csr.Name] = true
	}
}

// revokedError says that the node is revoked, and since when if the time is known.
func revokedError(name string, at time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("node %s is revoked", name)
	}
	return fmt.Errorf("node %s is revoked since %s", name, at.Format(revoked.TimeFormat))
}

// unreadRevoked is review's error for a request it left pending because the revoked nodes could
// not be read.
type unreadRevoked struct{ error }

func (u unreadRevoked) Unwrap() error { return u.error }

// Enforce applies the revoked nodes' admission policy when the ConfigMap of the revoked nodes
// changed since the policy was last applied from it, or at the first pass: so the policy follows
// a change made past chalkctl, as with kubectl, or one whose apply was cancelled. A failed apply
// is retried at the next pass.
func (a *Approver) Enforce(ctx context.Context) error {
	version := ""
	switch cm, err := a.Client.CoreV1().ConfigMaps(revoked.Namespace).Get(ctx, revoked.Name, metav1.GetOptions{}); {
	case err == nil:
		version = cm.ResourceVersion
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("read the revoked nodes: %w", err)
	}
	if a.enforced != nil && *a.enforced == version {
		return nil
	}
	if err := EnforceRevoked(ctx, a.Client); err != nil {
		return err
	}
	a.enforced = &version
	return nil
}
