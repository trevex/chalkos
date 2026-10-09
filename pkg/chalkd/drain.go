package chalkd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// UpgradeCordon marks a Node an upgrade cordoned, so the upgrade makes schedulable again only the
// nodes it cordoned itself.
const UpgradeCordon = "chalkos.dev/upgrade-cordon"

// defaultDrainTimeout is how long a drain waits for pods by default.
const defaultDrainTimeout = 5 * time.Minute

func (s *Server) DrainNode(ctx context.Context, req *connect.Request[nodev1.DrainNodeRequest]) (*connect.Response[nodev1.DrainNodeResponse], error) {
	cs, err := s.Kubernetes.upgradeClient()
	if err != nil {
		return nil, err
	}
	name := req.Msg.Node
	marked, err := setCordon(ctx, cs, name, true)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(req.Msg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = defaultDrainTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	evicted, kept, err := s.Kubernetes.evictPods(ctx, cs, name)
	if err != nil {
		return nil, failed(connect.CodeDeadlineExceeded, "drain %s: %v; it stays cordoned", name, err)
	}
	return connect.NewResponse(&nodev1.DrainNodeResponse{Marked: marked, Evicted: evicted, Kept: kept}), nil
}

func (s *Server) UncordonNode(ctx context.Context, req *connect.Request[nodev1.UncordonNodeRequest]) (*connect.Response[nodev1.UncordonNodeResponse], error) {
	cs, err := s.Kubernetes.upgradeClient()
	if err != nil {
		return nil, err
	}
	uncordoned, err := setCordon(ctx, cs, req.Msg.Node, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nodev1.UncordonNodeResponse{Uncordoned: uncordoned}), nil
}

// upgradeClient reaches the API server of a bootstrapped control-plane node.
func (k *Kubernetes) upgradeClient() (kubernetes.Interface, error) {
	c, _, share, err := k.member()
	if err != nil {
		return nil, err
	}
	cs, err := k.APIClient(c, share)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	return cs, nil
}

// setCordon cordons a node and marks it, unless it was cordoned unmarked, or uncordons a node
// marked so and removes the mark. It reports whether the node is marked (cordon) or was
// (uncordon).
func setCordon(ctx context.Context, cs kubernetes.Interface, name string, cordon bool) (bool, error) {
	var marked bool
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		_, marked = node.Annotations[UpgradeCordon]
		switch {
		case cordon && marked, cordon && node.Spec.Unschedulable:
			return nil
		case cordon:
			if node.Annotations == nil {
				node.Annotations = map[string]string{}
			}
			node.Annotations[UpgradeCordon] = "true"
			node.Spec.Unschedulable = true
			marked = true
		case !marked:
			return nil
		default:
			delete(node.Annotations, UpgradeCordon)
			node.Spec.Unschedulable = false
		}
		_, err = cs.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
	switch {
	case apierrors.IsNotFound(err):
		return false, failed(connect.CodeNotFound, "the cluster has no Node %s", name)
	case err != nil:
		return false, failed(connect.CodeUnavailable, "cordon %s: %v", name, err)
	}
	return marked, nil
}

// evictable reports whether a drain evicts the pod: one of a controller, but not of a
// DaemonSet, which would run it again on the node at once, and not a static pod's mirror. A pod
// without a controller would be gone for good; it stays, and runs again once the node is back.
func evictable(p corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return false
	}
	if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return false
	}
	owner := metav1.GetControllerOf(&p)
	return owner != nil && owner.Kind != "DaemonSet"
}

// evictPods evicts the node's evictable pods, again while an eviction would break a
// PodDisruptionBudget, and waits until they are gone.
func (k *Kubernetes) evictPods(ctx context.Context, cs kubernetes.Interface, node string) (evicted, kept []string, err error) {
	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String()})
	if err != nil {
		return nil, nil, err
	}
	pending := map[types.UID]corev1.Pod{}
	for _, p := range pods.Items {
		if p.Spec.NodeName != node {
			continue
		}
		name := p.Namespace + "/" + p.Name
		if !evictable(p) {
			kept = append(kept, name)
			continue
		}
		evicted = append(evicted, name)
		pending[p.UID] = p
	}
	slices.Sort(evicted)
	slices.Sort(kept)
	asked := map[types.UID]bool{}
	for len(pending) > 0 {
		var blocked []string
		for uid, p := range pending {
			if asked[uid] {
				continue
			}
			err := cs.PolicyV1().Evictions(p.Namespace).Evict(ctx, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}})
			switch {
			case err == nil:
				asked[uid] = true
			case apierrors.IsNotFound(err):
				delete(pending, uid)
			case apierrors.IsTooManyRequests(err):
				// A PodDisruptionBudget allows no disruption now; another pod may become ready.
				blocked = append(blocked, fmt.Sprintf("%s/%s: %v", p.Namespace, p.Name, err))
			default:
				return nil, nil, fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, err)
			}
		}
		for uid, p := range pending {
			current, err := cs.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) || err == nil && current.UID != uid {
				delete(pending, uid)
			}
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			var left []string
			for _, p := range pending {
				left = append(left, p.Namespace+"/"+p.Name)
			}
			slices.Sort(left)
			slices.Sort(blocked)
			why := "they did not stop"
			if len(blocked) > 0 {
				why = strings.Join(blocked, "; ")
			}
			return nil, nil, fmt.Errorf("pods %s still run: %s", strings.Join(left, ", "), why)
		case <-time.After(k.poll()):
		}
	}
	return evicted, kept, nil
}
