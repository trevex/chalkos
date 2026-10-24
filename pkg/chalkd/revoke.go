package chalkd

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	kapply "github.com/trevex/chalkos/pkg/kubernetes/apply"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
	"github.com/trevex/chalkos/pkg/pki"
)

func (s *Server) RevokeNode(ctx context.Context, req *connect.Request[nodev1.RevokeNodeRequest]) (*connect.Response[nodev1.RevokeNodeResponse], error) {
	name := req.Msg.Node
	if err := checkNodeName(name); err != nil {
		return nil, err
	}
	cs, _, err := s.Kubernetes.revokedClient()
	if err != nil {
		return nil, err
	}
	if err := policyRoom(ctx, cs, name); err != nil {
		return nil, err
	}
	at, err := revoked.Revoke(ctx, cs, name, time.Now())
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	if err := kapply.EnforceRevoked(ctx, cs); err != nil {
		return nil, failed(connect.CodeUnavailable, "%s is revoked, but the API server's admission policy was not updated: %v; revoking it again applies it", name, err)
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
	if err := checkNodeName(name); err != nil {
		return nil, err
	}
	cs, share, err := s.Kubernetes.revokedClient()
	if err != nil {
		return nil, err
	}
	// The CAs are checked against the time the node is listed with when it is removed, which
	// another control plane may have changed since a first read.
	var stale []string
	at, listed, err := revoked.Unrevoke(ctx, cs, name, func(at time.Time) error {
		var err error
		if stale, err = s.staleCAs(share, at); err != nil {
			return failed(connect.CodeInternal, "%v", err)
		}
		if len(stale) > 0 && !req.Msg.Force {
			return failed(connect.CodeFailedPrecondition, "%s; rotate them first, or pass --force to unrevoke %s anyway", staleWarning(name, at, stale), name)
		}
		return nil
	})
	if connectErr := (*connect.Error)(nil); errors.As(err, &connectErr) {
		return nil, connectErr
	}
	if err != nil {
		return nil, failed(connect.CodeUnavailable, "%v", err)
	}
	// A node unlisted already may be unrevoked again because the policy was not updated then.
	if err := kapply.EnforceRevoked(ctx, cs); err != nil {
		if !listed {
			return nil, failed(connect.CodeUnavailable, "node %s is not revoked, but the API server's admission policy was not updated: %v", name, err)
		}
		return nil, failed(connect.CodeUnavailable, "%s is unrevoked, but the API server's admission policy was not updated and still denies its writes: %v; unrevoking it again applies it", name, err)
	}
	if !listed {
		return nil, failed(connect.CodeNotFound, "node %s is not revoked", name)
	}
	if len(stale) > 0 {
		log.Printf("kubernetes: unrevoked node %s, forced: %s", name, staleWarning(name, at, stale))
	} else {
		log.Printf("kubernetes: unrevoked node %s", name)
	}
	resp := &nodev1.UnrevokeNodeResponse{StaleCas: stale}
	if !at.IsZero() {
		resp.RevokedAt = timestamppb.New(at)
	}
	return connect.NewResponse(resp), nil
}

// policyRoom refuses to revoke a node not revoked yet while the admission policy's map of revoked
// nodes would grow past revoked.MaxLiteralLength with it, which the API server could not compile.
func policyRoom(ctx context.Context, cs kubernetes.Interface, name string) error {
	list, err := revoked.Load(ctx, cs)
	if err != nil {
		return failed(connect.CodeUnavailable, "%v", err)
	}
	if _, ok := list[name]; ok {
		return nil
	}
	list[name] = time.Now()
	if n := revoked.LiteralLength(list); n > revoked.MaxLiteralLength {
		return failed(connect.CodeFailedPrecondition, "revoking %s would make the admission policy's map of the %d revoked nodes %d characters long, "+
			"past the %d chalkos allows below the API server's limit on an expression; make room first: rotate the CAs with chalkctl rotate os-ca "+
			"and chalkctl rotate kubernetes-ca, each to its finish, then unrevoke the nodes revoked before them with chalkctl node unrevoke",
			name, len(list), n, revoked.MaxLiteralLength)
	}
	return nil
}

// checkNodeName refuses what is no Kubernetes node name, which the revoked nodes would never
// match, such as W1 for w1.
func checkNodeName(name string) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return failed(connect.CodeInvalidArgument, "the node name %q is invalid: %s", name, strings.Join(errs, "; "))
	}
	return nil
}

// notRevoked refuses to renew the node certificate of a revoked node. Without the revoked nodes,
// the node may be among them: nothing is renewed until they can be read.
func (s *Server) notRevoked(ctx context.Context, name string) error {
	list, err := s.revokedNodes(ctx)
	if connectErr := (*connect.Error)(nil); errors.As(err, &connectErr) {
		return connectErr
	}
	if err != nil {
		return failed(connect.CodeUnavailable, "%v; no node certificate is renewed until they can be read", err)
	}
	at, ok := list[name]
	if !ok {
		return nil
	}
	since := ""
	if !at.IsZero() {
		since = " since " + at.UTC().Format(time.RFC3339)
	}
	return failed(connect.CodePermissionDenied, "node %s is revoked%s; chalkctl node unrevoke %s lets it renew again", name, since, name)
}

// revokedClient reaches the API server of a bootstrapped control-plane node, which keeps the
// revoked nodes, and returns the node's share.
func (k *Kubernetes) revokedClient() (kubernetes.Interface, kpki.Share, error) {
	if k == nil {
		return nil, kpki.Share{}, failed(connect.CodeFailedPrecondition, "the node's image has no Kubernetes, so the node keeps no revoked nodes; "+
			"a node without Kubernetes is revoked by not renewing its node certificate, and chalkctl rotate os-ca cuts it off at once")
	}
	cs, cp, err := k.apiClient(k.etcdMember)
	return cs, cp.share, err
}

// revokedNodes returns the cluster's revoked nodes, as RevokedNodes does when set.
func (s *Server) revokedNodes(ctx context.Context) (revoked.List, error) {
	if s.RevokedNodes != nil {
		return s.RevokedNodes(ctx)
	}
	return s.Kubernetes.revokedNodes(ctx)
}

// revokedNodes reads the revoked nodes through the control plane's own API server.
func (k *Kubernetes) revokedNodes(ctx context.Context) (revoked.List, error) {
	cs, _, err := k.apiClient(k.controlPlane)
	if err != nil {
		return nil, err
	}
	return revoked.Load(ctx, cs)
}

// revokedStatus lists the revoked nodes in a bootstrapped control plane's status, or why they
// could not be read.
func (k *Kubernetes) revokedStatus(ctx context.Context, st *nodev1.KubernetesStatus) {
	list, err := k.revokedNodes(ctx)
	if err != nil {
		st.RevokedNodesError = err.Error()
		return
	}
	st.RevokedNodesRead = true
	for _, name := range slices.Sorted(maps.Keys(list)) {
		n := &nodev1.RevokedNode{Name: name}
		if at := list[name]; !at.IsZero() {
			n.RevokedAt = timestamppb.New(at)
		}
		st.RevokedNodes = append(st.RevokedNodes, n)
	}
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
		stale = append(stale, string(clustersecrets.RotateOSCA))
	}
	if madeBefore(kubernetesCAs, at) {
		stale = append(stale, string(clustersecrets.RotateKubernetesCA))
	}
	return stale, nil
}

// madeBefore reports whether any of the CAs was made no later than at; every CA was when at is
// unknown, the zero time.
func madeBefore(cas []*x509.Certificate, at time.Time) bool {
	if at.IsZero() {
		return len(cas) > 0
	}
	for _, ca := range cas {
		if !pki.IssuedAt(ca).After(at) {
			return true
		}
	}
	return false
}

// staleWarning says what unrevoking a node revoked at means while the stale CAs are trusted.
func staleWarning(name string, at time.Time, stale []string) string {
	described := map[string]string{string(clustersecrets.RotateOSCA): "the OS CA", string(clustersecrets.RotateKubernetesCA): "the Kubernetes CA"}
	var cas []string
	for _, ca := range stale {
		cas = append(cas, fmt.Sprintf("%s (chalkctl rotate %s)", described[ca], ca))
	}
	when := "at an unknown time"
	if !at.IsZero() {
		when = "at " + at.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s was revoked %s, but %s may predate the revocation: the old node's certificates would work again until they expire",
		name, when, strings.Join(cas, " and "))
}
