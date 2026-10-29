package chalkd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/renewal"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/pki"
)

func (s *Server) RenewNodeCertificate(ctx context.Context, req *connect.Request[nodev1.RenewNodeCertificateRequest]) (*connect.Response[nodev1.RenewNodeCertificateResponse], error) {
	peer, _ := ctx.Value(peerKey{}).(*x509.Certificate)
	if peer == nil {
		return nil, failed(connect.CodeUnauthenticated, "renewing a node certificate needs the node's current one")
	}
	nodeCA, err := s.nodeCA()
	if err != nil {
		return nil, err
	}
	names := pki.NamesOf(peer)
	if err := s.notRevoked(ctx, names.CommonName); err != nil {
		return nil, err
	}
	chain, err := signRequest(nodeCA, names, req.Msg.CertificateRequest, time.Now())
	if err != nil {
		return nil, err
	}
	log.Printf("issued node %s a new node certificate", peer.Subject.CommonName)
	return connect.NewResponse(&nodev1.RenewNodeCertificateResponse{CertificateChain: []byte(chain)}), nil
}

// nodeCA returns the node CA of a control-plane node's share; only control planes hold it.
func (s *Server) nodeCA() (pki.CertKey, error) {
	notControlPlane := failed(connect.CodeFailedPrecondition, "the node is not a control plane; control-plane nodes renew node certificates")
	if s.Kubernetes == nil {
		return pki.CertKey{}, notControlPlane
	}
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil {
		return pki.CertKey{}, failed(connect.CodeInternal, "%v", err)
	}
	if c.Kind != k8s.KindControlPlane {
		return pki.CertKey{}, notControlPlane
	}
	// A node that left etcd is no control plane of the cluster any more, though its share still
	// holds the node CA until it is reinstalled.
	if m, _, err := knode.ReadMembership(s.Kubernetes.Paths); err != nil {
		return pki.CertKey{}, failed(connect.CodeInternal, "%v", err)
	} else if m == knode.MembershipLeft {
		return pki.CertKey{}, failed(connect.CodeFailedPrecondition, "the node left etcd and signs no node certificates; reinstall it to join the cluster again")
	}
	share, err := knode.ReadShare(s.Kubernetes.Paths)
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
func (s *Server) IssueNodeCertificate(ctx context.Context, request []byte) (string, error) {
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil {
		return "", err
	}
	if c.Kind == k8s.KindControlPlane {
		nodeCA, err := s.nodeCA()
		if err != nil {
			return "", err
		}
		// A control plane removed from etcd without leaving it still holds the node CA; once it
		// is revoked, it renews its own certificate no more than another node's.
		names := pki.NamesOf(s.Certificate.Current().Leaf)
		if err := s.notRevoked(ctx, names.CommonName); err != nil {
			return "", err
		}
		return signRequest(nodeCA, names, request, time.Now())
	}
	return RenewThrough(ctx, s.Certificate, net.JoinHostPort(c.EndpointHost(), client.Port), request)
}

// RenewThrough asks the chalkd at addr, which must present a node certificate, for a node
// certificate, authenticating with the current one. Any node certificate is accepted: the
// endpoint may be a VIP or a name no node certificate carries. Whatever the peer returns is
// checked like a delivered certificate before it is used.
func RenewThrough(ctx context.Context, cert *NodeCertificate, addr string, request []byte) (string, error) {
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
	resp, err := conn.RenewNodeCertificate(ctx, connect.NewRequest(&nodev1.RenewNodeCertificateRequest{CertificateRequest: request}))
	if err != nil {
		return "", fmt.Errorf("the control plane at %s: %w", addr, err)
	}
	return string(resp.Msg.CertificateChain), nil
}

// NewNodeRenewal renews the node certificate once two thirds of its lifetime have passed, with a
// certificate issue obtains for a new key.
func NewNodeRenewal(cert *NodeCertificate, issue func(context.Context, []byte) (string, error)) *renewal.Renewal {
	due := func(now time.Time) (bool, time.Time, error) {
		leaf := cert.Current().Leaf
		return !now.Before(pki.RenewAt(leaf)), leaf.NotAfter, nil
	}
	return renewal.New("node certificate", due, func(ctx context.Context, now time.Time) error {
		return renewNodeCertificate(ctx, cert, issue, now)
	})
}

// renewNodeCertificate obtains a node certificate for a new key, checks it is for exactly the
// current certificate's names and lasts longer, and switches to it.
func renewNodeCertificate(ctx context.Context, cert *NodeCertificate, issue func(context.Context, []byte) (string, error), now time.Time) error {
	current := cert.Current().Leaf
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: current.Subject.CommonName}}, key)
	if err != nil {
		return err
	}
	chain, err := issue(ctx, request)
	if err != nil {
		return err
	}
	leaf, err := pki.ParseCertificate([]byte(chain))
	if err != nil {
		return fmt.Errorf("the issued certificate: %w", err)
	}
	if !pki.NamesOf(leaf).Equal(pki.NamesOf(current)) {
		return errors.New("the issued certificate is for other names than the node's")
	}
	if !leaf.NotAfter.After(current.NotAfter) {
		return fmt.Errorf("the issued certificate expires %s, no later than the current one", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return err
	}
	// ReplaceFrom checks that the key is the new one and that the chain leads to the OS CA.
	replaced, err := cert.ReplaceFrom(current, chain, keyPEM, now)
	if err != nil {
		return err
	}
	if !replaced {
		log.Print("node certificate: another one was delivered while the renewal ran; keeping it")
	}
	return nil
}
