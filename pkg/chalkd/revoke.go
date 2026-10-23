package chalkd

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
	"github.com/trevex/chalkos/pkg/pki"
)

func (s *Server) RevokeNode(ctx context.Context, req *connect.Request[nodev1.RevokeNodeRequest]) (*connect.Response[nodev1.RevokeNodeResponse], error) {
	name := req.Msg.Node
	if name == "" {
		return nil, failed(connect.CodeInvalidArgument, "name the node to revoke")
	}
	cs, _, err := s.Kubernetes.revokedClient()
	if err != nil {
		return nil, err
	}
	at, err := revoked.Revoke(ctx, cs, name, time.Now())
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	deleted := true
	switch err := cs.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{}); {
	case apierrors.IsNotFound(err):
		deleted = false
	case err != nil:
		return nil, failed(connect.CodeUnavailable, "%s is revoked, but deleting its Node failed: %v; revoking it again deletes it", name, err)
	}
	log.Printf("kubernetes: revoked node %s", name)
	return connect.NewResponse(&nodev1.RevokeNodeResponse{RevokedAt: timestamppb.New(at), NodeDeleted: deleted}), nil
}

func (s *Server) UnrevokeNode(ctx context.Context, req *connect.Request[nodev1.UnrevokeNodeRequest]) (*connect.Response[nodev1.UnrevokeNodeResponse], error) {
	name := req.Msg.Node
	if name == "" {
		return nil, failed(connect.CodeInvalidArgument, "name the node to unrevoke")
	}
	cs, share, err := s.Kubernetes.revokedClient()
	if err != nil {
		return nil, err
	}
	list, err := revoked.Load(ctx, cs)
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	at, ok := list[name]
	if !ok {
		return nil, failed(connect.CodeNotFound, "node %s is not revoked", name)
	}
	stale, err := s.staleCAs(share, at)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if len(stale) > 0 && !req.Msg.Force {
		return nil, failed(connect.CodeFailedPrecondition, "%s; rotate them first, or pass --force to unrevoke %s anyway", staleWarning(name, at, stale), name)
	}
	if _, _, err := revoked.Unrevoke(ctx, cs, name); err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	if len(stale) > 0 {
		log.Printf("kubernetes: unrevoked node %s, forced: %s", name, staleWarning(name, at, stale))
	} else {
		log.Printf("kubernetes: unrevoked node %s", name)
	}
	return connect.NewResponse(&nodev1.UnrevokeNodeResponse{RevokedAt: timestamppb.New(at), StaleCas: stale}), nil
}

// revokedClient reaches the API server of a bootstrapped control-plane node, which keeps the
// revoked nodes, and returns the node's share.
func (k *Kubernetes) revokedClient() (kubernetes.Interface, kpki.Share, error) {
	if k == nil {
		return nil, kpki.Share{}, failed(connect.CodeFailedPrecondition, "the node's image has no Kubernetes, so the node keeps no revoked nodes; "+
			"a node without Kubernetes is revoked by not renewing its node certificate, and chalkctl rotate os-ca cuts it off at once")
	}
	c, _, share, err := k.member()
	if err != nil {
		return nil, kpki.Share{}, err
	}
	cs, err := k.APIClient(c, share)
	if err != nil {
		return nil, kpki.Share{}, failed(connect.CodeInternal, "%v", err)
	}
	return cs, share, nil
}

// revokedNodes returns the cluster's revoked nodes, as RevokedNodes does when set.
func (s *Server) revokedNodes(ctx context.Context) (revoked.List, error) {
	if s.RevokedNodes != nil {
		return s.RevokedNodes(ctx)
	}
	c, _, share, err := s.Kubernetes.controlPlane()
	if err != nil {
		return nil, err
	}
	cs, err := s.Kubernetes.APIClient(c, share)
	if err != nil {
		return nil, err
	}
	return revoked.Load(ctx, cs)
}

// staleCAs names, as chalkctl rotate does, the CAs made before a node was revoked at that its
// certificates may chain to. chalkd verifies a node against the OS CAs alone, so a node CA any
// of them signed, rotated or not, still vouches for it; the kubelet's certificates chain to the
// Kubernetes CAs.
func (s *Server) staleCAs(share kpki.Share, at time.Time) ([]string, error) {
	if s.Certificate == nil {
		return nil, errors.New("the node trusts no OS CA")
	}
	osCAs, err := pki.ParseBundle(s.Certificate.OSCA())
	if err != nil {
		return nil, fmt.Errorf("the OS CA: %w", err)
	}
	trusted := []string{share.CA.Certificate}
	if share.Accepted != nil {
		trusted = append(trusted, share.Accepted.CA...)
	}
	kubernetesCAs, err := pki.ParseBundle(pki.Bundle(trusted...))
	if err != nil {
		return nil, fmt.Errorf("the Kubernetes CA: %w", err)
	}
	var stale []string
	if madeBefore(osCAs, at) {
		stale = append(stale, pki.RotateOSCA)
	}
	if madeBefore(kubernetesCAs, at) {
		stale = append(stale, pki.RotateKubernetesCA)
	}
	return stale, nil
}

// madeBefore reports whether any of the CAs was made no later than at.
func madeBefore(cas []*x509.Certificate, at time.Time) bool {
	for _, ca := range cas {
		if !pki.IssuedAt(ca).After(at) {
			return true
		}
	}
	return false
}

// staleWarning says what unrevoking a node revoked at means while the stale CAs are trusted.
func staleWarning(name string, at time.Time, stale []string) string {
	described := map[string]string{pki.RotateOSCA: "the OS CA", pki.RotateKubernetesCA: "the Kubernetes CA"}
	var cas []string
	for _, ca := range stale {
		cas = append(cas, fmt.Sprintf("%s (chalkctl rotate %s)", described[ca], ca))
	}
	return fmt.Sprintf("%s was revoked at %s, but %s predate the revocation: the old node's certificates would work again until they expire",
		name, at.UTC().Format(time.RFC3339), strings.Join(cas, " and "))
}
