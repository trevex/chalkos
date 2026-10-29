package chalkd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/pki"
)

var _ layer.Issuer = (*kubernetesLayer)(nil)

func (p *kubernetesLayer) RenewNodeCertificate(ctx context.Context, req *connect.Request[kubernetesv1.RenewNodeCertificateRequest]) (*connect.Response[kubernetesv1.RenewNodeCertificateResponse], error) {
	peer := layer.Peer(ctx)
	if peer == nil {
		return nil, failed(connect.CodeUnauthenticated, "renewing a node certificate needs the node's current one")
	}
	nodeCA, err := p.k.nodeCA()
	if err != nil {
		return nil, err
	}
	names := pki.NamesOf(peer)
	if err := p.k.notRevoked(ctx, names.CommonName); err != nil {
		return nil, err
	}
	chain, err := signRequest(nodeCA, names, req.Msg.CertificateRequest, time.Now())
	if err != nil {
		return nil, err
	}
	log.Printf("issued node %s a new node certificate", peer.Subject.CommonName)
	return connect.NewResponse(&kubernetesv1.RenewNodeCertificateResponse{CertificateChain: []byte(chain)}), nil
}

// nodeCA returns the node CA of a control-plane node's share; only control planes hold it.
func (k *Kubernetes) nodeCA() (pki.CertKey, error) {
	notControlPlane := failed(connect.CodeFailedPrecondition, "the node is not a control plane; control-plane nodes renew node certificates")
	if k == nil {
		return pki.CertKey{}, notControlPlane
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return pki.CertKey{}, failed(connect.CodeInternal, "%v", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return pki.CertKey{}, notControlPlane
	}
	// A node that left etcd is no control plane of the cluster any more, though its share still
	// holds the node CA until it is reinstalled.
	if m, _, err := knode.ReadMembership(k.Paths); err != nil {
		return pki.CertKey{}, failed(connect.CodeInternal, "%v", err)
	} else if m == knode.MembershipLeft {
		return pki.CertKey{}, failed(connect.CodeFailedPrecondition, "the node left etcd and signs no node certificates; reinstall it to join the cluster again")
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return pki.CertKey{}, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	if share.NodeCA == nil {
		return pki.CertKey{}, failed(connect.CodeFailedPrecondition, "the node's share holds no node CA")
	}
	return *share.NodeCA, nil
}

// signRequest issues a node certificate for the key of a PKCS #10 request, for the names given:
// the request's own names are ignored, so a node renews exactly the names it was issued.
func signRequest(nodeCA pki.CertKey, names pki.NodeNames, request []byte, now time.Time) (string, error) {
	csr, err := x509.ParseCertificateRequest(request)
	if err != nil {
		return "", failed(connect.CodeInvalidArgument, "parse the certificate request: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return "", failed(connect.CodeInvalidArgument, "the certificate request is not signed with its key: %v", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return "", failed(connect.CodeInvalidArgument, "the certificate request's key is not ECDSA P-256")
	}
	chain, err := pki.SignNode(nodeCA, names, pub, now)
	if err != nil {
		return "", failed(connect.CodeInternal, "issue the node certificate: %v", err)
	}
	return chain, nil
}

// IssueNodeCertificate obtains a node certificate for the certificate request: from the node's own
// node CA on a control plane, from the control plane at the cluster's endpoint on a worker.
func (p *kubernetesLayer) IssueNodeCertificate(ctx context.Context, request []byte) (string, error) {
	c, err := k8s.ReadCluster(p.k.Paths.Cluster)
	if err != nil {
		return "", err
	}
	if c.Kind == k8s.KindControlPlane {
		nodeCA, err := p.k.nodeCA()
		if err != nil {
			return "", err
		}
		// A control plane removed from etcd without leaving it still holds the node CA; once it
		// is revoked, it renews its own certificate no more than another node's.
		names := pki.NamesOf(p.node.NodeCertificate().Current().Leaf)
		if err := p.k.notRevoked(ctx, names.CommonName); err != nil {
			return "", err
		}
		return signRequest(nodeCA, names, request, time.Now())
	}
	return RenewThrough(ctx, p.node.NodeCertificate(), net.JoinHostPort(c.EndpointHost(), client.Port), request)
}

// RenewThrough asks the chalkd at addr, which must present a node certificate, for a node
// certificate, authenticating with the current one. Any node certificate is accepted: the
// endpoint may be a VIP or a name no node certificate carries. Whatever the peer returns is
// checked like a delivered certificate before it is used.
func RenewThrough(ctx context.Context, cert layer.Certificate, addr string, request []byte) (string, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cert.OSCA())) {
		return "", errors.New("the node's OS CA holds no certificate")
	}
	conn, err := client.Dial(addr, client.Options{Verify: client.AnyNodeOf(roots), GetClientCertificate: cert.GetClientCertificate})
	if err != nil {
		return "", err
	}
	// Renewals are hours apart: nothing is kept open in between.
	defer conn.Close()
	k := kubernetesv1connect.NewKubernetesServiceClient(conn.HTTPClient(), conn.BaseURL())
	resp, err := k.RenewNodeCertificate(ctx, connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{CertificateRequest: request}))
	if err != nil {
		return "", fmt.Errorf("the control plane at %s: %w", addr, err)
	}
	return string(resp.Msg.CertificateChain), nil
}
