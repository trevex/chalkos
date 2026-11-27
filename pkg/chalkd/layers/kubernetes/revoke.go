package kubernetes

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

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/strs"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kapply "github.com/trevex/chalkos/pkg/kubernetes/apply"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
	"github.com/trevex/chalkos/pkg/pki"
)

func (p *Plugin) RevokeNode(ctx context.Context, req *kubernetesv1.RevokeNodeRequest) (*kubernetesv1.RevokeNodeResponse, error) {
	name := req.Node
	if err := checkNodeName(name); err != nil {
		return nil, err
	}
	// A bootstrapped control plane's API server keeps the revoked nodes.
	cs, cp, err := p.k.apiClient(p.k.etcdMember)
	if err != nil {
		return nil, err
	}
	// Dropping the expired nodes first makes room for the node.
	pruneExpiredLogged(ctx, cs, cp.cluster)
	list, err := policyRoom(ctx, cs, name)
	if err != nil {
		return nil, err
	}
	at, err := revoked.Revoke(ctx, cs, name, time.Now())
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	if err := kapply.EnforceRevoked(ctx, cs); err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%s is revoked, but the API server's admission policy was not updated: %w; revoking it again applies it", name, err)
	}
	deleted := true
	switch err := cs.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{}); {
	case apierrors.IsNotFound(err):
		deleted = false
	case err != nil:
		return nil, layer.Errorf(connect.CodeUnavailable, "%s is revoked, but deleting its Node failed: %w; revoking it again deletes it", name, err)
	}
	log.Printf("kubernetes: revoked node %s", name)
	list[name] = at
	return &kubernetesv1.RevokeNodeResponse{
		RevokedAt:    timestamppb.New(at),
		NodeDeleted:  deleted,
		ListLength:   uint32(revoked.LiteralLength(list)),
		ListCapacity: revoked.MaxLiteralLength,
	}, nil
}

func (p *Plugin) UnrevokeNode(ctx context.Context, req *kubernetesv1.UnrevokeNodeRequest) (*kubernetesv1.UnrevokeNodeResponse, error) {
	name := req.Node
	if err := checkNodeName(name); err != nil {
		return nil, err
	}
	cs, cp, err := p.k.apiClient(p.k.etcdMember)
	if err != nil {
		return nil, err
	}
	// The CAs are checked against the time the node is listed with when it is removed, which
	// another control plane may have changed since a first read.
	var stale []string
	at, listed, err := revoked.Unrevoke(ctx, cs, name, func(at time.Time) error {
		var err error
		if stale, err = p.staleCAs(cp.share, at); err != nil {
			return layer.Errorf(connect.CodeInternal, "%w", err)
		}
		if len(stale) > 0 && !req.Force {
			return layer.Errorf(connect.CodeFailedPrecondition, "%s; rotate them first, or pass --allow-old-ca to unrevoke %s anyway", staleWarning(name, at, stale), name)
		}
		return nil
	})
	if _, ok := errors.AsType[layer.Coded](err); ok {
		return nil, err
	}
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	// A node unlisted already may be unrevoked again because the policy was not updated then.
	if err := kapply.EnforceRevoked(ctx, cs); err != nil {
		if !listed {
			return nil, layer.Errorf(connect.CodeUnavailable, "node %s is not revoked, but the API server's admission policy was not updated: %w", name, err)
		}
		return nil, layer.Errorf(connect.CodeUnavailable, "%s is unrevoked, but the API server's admission policy was not updated and still denies its writes: %w; unrevoking it again applies it", name, err)
	}
	if !listed {
		return nil, layer.Errorf(connect.CodeNotFound, "node %s is not revoked", name)
	}
	pruneExpiredLogged(ctx, cs, cp.cluster)
	if len(stale) > 0 {
		log.Printf("kubernetes: unrevoked node %s, forced: %s", name, staleWarning(name, at, stale))
	} else {
		log.Printf("kubernetes: unrevoked node %s", name)
	}
	resp := &kubernetesv1.UnrevokeNodeResponse{StaleCas: stale}
	if !at.IsZero() {
		resp.RevokedAt = timestamppb.New(at)
	}
	return resp, nil
}

// policyRoom refuses, with the reason REVOKED_LIST_FULL, to revoke a node not revoked yet while
// the admission policy's map of revoked nodes would grow past revoked.MaxLiteralLength with it,
// which the API server could not compile. It returns the revoked nodes as it read them, with the
// node among them.
func policyRoom(ctx context.Context, cs kubernetes.Interface, name string) (revoked.List, error) {
	list, err := revoked.Load(ctx, cs)
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	if _, ok := list[name]; ok {
		return list, nil
	}
	list[name] = time.Now()
	if n := revoked.LiteralLength(list); n > revoked.MaxLiteralLength {
		return nil, layer.Reasonf(connect.CodeFailedPrecondition, nodev1.ErrorReason_ERROR_REASON_REVOKED_LIST_FULL,
			"revoking %s would make the admission policy's map of the %d revoked nodes %d characters long, "+
				"past the %d chalkos allows below the API server's limit on an expression; make room: rotate the CAs with "+
				"chalkctl rotate os-ca and chalkctl rotate kubernetes-ca, each to its finish, which drops the nodes revoked before both",
			name, len(list), n, revoked.MaxLiteralLength)
	}
	return list, nil
}

// certValidity is how long a node's certificates are valid: chalkos's node certificates for
// pki.LeafValidity, the kubelet's client certificates for the controller-manager's
// cluster-signing-duration, a year unless the Cluster's extra flags set it longer. It reads the
// Cluster as the control plane has it now, so lowering the flag lets entries expire earlier,
// although certificates signed before still last as long as they were signed for.
func certValidity(c k8s.Cluster) time.Duration {
	v := pki.LeafValidity
	for name, value := range c.ExtraArgs["kube-controller-manager"] {
		if k8s.FlagName(name) != "cluster-signing-duration" {
			continue
		}
		if d, err := time.ParseDuration(value); err == nil && d > v {
			v = d
		}
	}
	return v
}

// dropExpired drops the revoked nodes whose certificates have all expired by now, when the
// cluster c signs them for certValidity, and applies the admission policy again when it dropped
// one.
//
// Each control plane drops expired nodes by itself, through its own API server: when it revokes
// or unrevokes a node, and every Timing.PruneRevoked. That rule depends on the entry alone. Nodes
// whose certificates chain only to CAs made after their revocation are dropped only by
// PruneRevoked, called once every node confirmed the finish of a CA's rotation: control planes
// finish a rotation one after another, and an entry one of them could drop for its CAs may still
// keep a node out of another that trusts an old CA yet.
func dropExpired(ctx context.Context, cs kubernetes.Interface, c k8s.Cluster) error {
	validity, now := certValidity(c), time.Now()
	removed, err := revoked.Prune(ctx, cs, func(_ string, at time.Time) bool {
		return revoked.Expired(at, now, validity)
	})
	if err != nil || len(removed) == 0 {
		return err
	}
	log.Printf("kubernetes: dropped the revoked nodes %s: their certificates expired", strings.Join(removed, ", "))
	return kapply.EnforceRevoked(ctx, cs)
}

// pruneExpired drops, through the node's own API server, the revoked nodes whose certificates
// have all expired.
func (k *side) pruneExpired(ctx context.Context) error {
	cs, cp, err := k.apiClient(k.etcdMember)
	if err != nil {
		return err
	}
	return dropExpired(ctx, cs, cp.cluster)
}

// pruneExpiredLogged drops the expired revoked nodes and logs a failure, which holds up no
// revocation or unrevocation.
func pruneExpiredLogged(ctx context.Context, cs kubernetes.Interface, c k8s.Cluster) {
	if err := dropExpired(ctx, cs, c); err != nil {
		log.Printf("kubernetes: %v", err)
	}
}

// PruneRevoked drops the revoked nodes whose certificates have all expired, and those revoked
// before every OS CA and Kubernetes CA the control plane trusts was made, whose certificates
// chain to no trusted CA. It applies the admission policy again even when it dropped none, so
// calling it again applies a policy an earlier call failed to.
func (p *Plugin) PruneRevoked(ctx context.Context, _ *kubernetesv1.PruneRevokedRequest) (*kubernetesv1.PruneRevokedResponse, error) {
	cs, cp, err := p.k.apiClient(p.k.etcdMember)
	if err != nil {
		return nil, err
	}
	osCAs, kubernetesCAs, err := p.trustedCAs(cp.share)
	if err != nil {
		return nil, layer.Errorf(connect.CodeInternal, "%w", err)
	}
	validity, now := certValidity(cp.cluster), time.Now()
	removed, err := revoked.Prune(ctx, cs, func(_ string, at time.Time) bool {
		return revoked.Expired(at, now, validity) || len(staleAt(osCAs, kubernetesCAs, at)) == 0
	})
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	if len(removed) > 0 {
		log.Printf("kubernetes: dropped the revoked nodes %s: their certificates expired or chain only to CAs made since", strings.Join(removed, ", "))
	}
	if err := kapply.EnforceRevoked(ctx, cs); err != nil {
		dropped := "no revoked node was dropped"
		if len(removed) > 0 {
			dropped = "the revoked nodes " + strings.Join(removed, ", ") + " were dropped"
		}
		return nil, layer.Errorf(connect.CodeUnavailable, "%s, but the API server's admission policy was not updated: %w; pruning again applies it", dropped, err)
	}
	list, err := revoked.Load(ctx, cs)
	if err != nil {
		return nil, layer.Errorf(connect.CodeUnavailable, "%w", err)
	}
	return &kubernetesv1.PruneRevokedResponse{
		Pruned:       removed,
		ListLength:   uint32(revoked.LiteralLength(list)),
		ListCapacity: revoked.MaxLiteralLength,
	}, nil
}

// pruneRevokedEvery prunes the revoked nodes with hooks.PruneRevoked now and every
// timing.PruneRevoked until ctx ends. A failed prune, as while the API server does not answer yet
// after a restart, is tried again after timing.Poll, then after twice as long each time, up to
// timing.PruneRevoked. The first failure of a streak and the prune that ends it are logged.
func (k *side) pruneRevokedEvery(ctx context.Context) {
	failed, retry := 0, k.timing.Poll
	for {
		wait := k.timing.PruneRevoked
		switch err := k.hooks.PruneRevoked(ctx); {
		case ctx.Err() != nil:
			return
		case err != nil:
			if failed == 0 {
				log.Printf("kubernetes: %v; trying again in %v, then less and less often", err, retry)
			}
			failed++
			wait, retry = retry, min(2*retry, k.timing.PruneRevoked)
		case failed > 0:
			log.Printf("kubernetes: pruned the revoked nodes after %s", strs.Count(failed, "failed attempt"))
			failed, retry = 0, k.timing.Poll
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// checkNodeName refuses what is no Kubernetes node name, which the revoked nodes would never
// match, such as W1 for w1.
func checkNodeName(name string) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return layer.Errorf(connect.CodeInvalidArgument, "the node name %q is invalid: %s", name, strings.Join(errs, "; "))
	}
	return nil
}

// notRevoked refuses to renew the node certificate of a revoked node. Without the revoked nodes,
// the node may be among them: nothing is renewed until they can be read.
func (k *side) notRevoked(ctx context.Context, name string) error {
	list, err := k.hooks.RevokedNodes(ctx)
	if _, ok := errors.AsType[layer.Coded](err); ok {
		return err
	}
	if err != nil {
		return layer.Errorf(connect.CodeUnavailable, "%w; no node certificate is renewed until they can be read", err)
	}
	at, ok := list[name]
	if !ok {
		return nil
	}
	since := ""
	if !at.IsZero() {
		since = " since " + at.UTC().Format(time.RFC3339)
	}
	return layer.Errorf(connect.CodePermissionDenied, "node %s is revoked%s; chalkctl node unrevoke %s lets it renew again", name, since, name)
}

// loadRevoked reads the revoked nodes through the control plane's own API server.
func (k *side) loadRevoked(ctx context.Context) (revoked.List, error) {
	cs, _, err := k.apiClient(k.controlPlane)
	if err != nil {
		return nil, err
	}
	return revoked.Load(ctx, cs)
}

// revokedStatus lists the revoked nodes in a bootstrapped control plane's status, with how full
// their list is, or why they could not be read.
func (k *side) revokedStatus(ctx context.Context, st *kubernetesv1.StatusResponse) {
	st.RevokedListCapacity = revoked.MaxLiteralLength
	list, err := k.loadRevoked(ctx)
	if err != nil {
		st.RevokedNodesError = err.Error()
		return
	}
	for _, name := range slices.Sorted(maps.Keys(list)) {
		n := &kubernetesv1.RevokedNode{Name: name}
		if at := list[name]; !at.IsZero() {
			n.RevokedAt = timestamppb.New(at)
		}
		st.RevokedNodes = append(st.RevokedNodes, n)
	}
	st.RevokedListLength = uint32(revoked.LiteralLength(list))
}

// trustedCAs are the OS CAs the node trusts and the Kubernetes CAs of the share: the one that
// issues and those accepted during a rotation.
func (p *Plugin) trustedCAs(share kpki.Share) (osCAs, kubernetesCAs []*x509.Certificate, err error) {
	cert := p.node.NodeCertificate()
	if cert == nil {
		return nil, nil, errors.New("the node trusts no OS CA")
	}
	if osCAs, err = pki.ParseBundle(cert.OSCA()); err != nil {
		return nil, nil, fmt.Errorf("the OS CA: %w", err)
	}
	trusted := []string{share.CA.Certificate}
	if share.Accepted != nil {
		trusted = append(trusted, share.Accepted.CA...)
	}
	if kubernetesCAs, err = pki.ParseBundle(pki.Bundle(trusted...)); err != nil {
		return nil, nil, fmt.Errorf("the Kubernetes CA: %w", err)
	}
	return osCAs, kubernetesCAs, nil
}

// staleAt names, as chalkctl rotate does, the CAs made before a node was revoked at that its
// certificates may chain to. chalkd verifies a node against the OS CAs alone, so a node CA any
// of them signed, rotated or not, still vouches for it; the kubelet's certificates chain to the
// Kubernetes CAs. At the zero time, an unknown one, every CA is stale.
func staleAt(osCAs, kubernetesCAs []*x509.Certificate, at time.Time) []string {
	var stale []string
	if madeBefore(osCAs, at) {
		stale = append(stale, string(clustersecrets.RotateOSCA))
	}
	if madeBefore(kubernetesCAs, at) {
		stale = append(stale, string(clustersecrets.RotateKubernetesCA))
	}
	return stale
}

// staleCAs is staleAt for the CAs the node trusts.
func (p *Plugin) staleCAs(share kpki.Share, at time.Time) ([]string, error) {
	osCAs, kubernetesCAs, err := p.trustedCAs(share)
	if err != nil {
		return nil, err
	}
	return staleAt(osCAs, kubernetesCAs, at), nil
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
