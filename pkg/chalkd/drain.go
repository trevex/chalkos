package chalkd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	d, err := s.Kubernetes.evictPods(ctx, cs, name, req.Msg.DeleteEmptydirData)
	if err != nil {
		var de *drainError
		if errors.As(err, &de) {
			return nil, failed(de.code, "drain %s: %v; it stays cordoned", name, de.err)
		}
		return nil, failed(connect.CodeUnavailable, "drain %s: %v; it stays cordoned", name, err)
	}
	return connect.NewResponse(&nodev1.DrainNodeResponse{Marked: marked, Evicted: d.evicted, Kept: d.kept, Unmanaged: d.unmanaged}), nil
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
// marked so and removes the mark. A marked node is cordoned again when it was made schedulable
// since. It reports whether the node is marked (cordon) or was (uncordon).
func setCordon(ctx context.Context, cs kubernetes.Interface, name string, cordon bool) (bool, error) {
	var marked bool
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		_, marked = node.Annotations[UpgradeCordon]
		switch {
		case cordon && node.Spec.Unschedulable:
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

// drained is what a drain did with the node's pods, as namespace/name.
type drained struct {
	evicted, kept, unmanaged []string
}

// drainError is why a drain failed, with the code that tells a client whether another control
// plane might do better.
type drainError struct {
	code connect.Code
	err  error
}

func (e *drainError) Error() string { return e.err.Error() }

// evictPods evicts the node's pods of controllers but DaemonSets, again while an eviction would
// break a PodDisruptionBudget or the API server fails it, and waits until they are gone. It keeps
// DaemonSet pods, which would run again on the node at once, static pods' mirrors, finished pods,
// and pods without a controller, which would be gone for good and run again once the node is
// back. Pods that terminate already, as those an earlier drain evicted, are not evicted again but
// waited for too: the node is drained only once they have stopped. Pods with emptyDir volumes
// are evicted only when their data may be lost; otherwise nothing is evicted.
func (k *Kubernetes) evictPods(ctx context.Context, cs kubernetes.Interface, node string, deleteEmptyDir bool) (drained, error) {
	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String()})
	if err != nil {
		return drained{}, err
	}
	var d drained
	var emptyDir []string
	pending := map[types.UID]corev1.Pod{}
	asked := map[types.UID]bool{}
	for _, p := range pods.Items {
		if p.Spec.NodeName != node {
			continue
		}
		if p.DeletionTimestamp != nil {
			pending[p.UID], asked[p.UID] = p, true
			continue
		}
		name := p.Namespace + "/" + p.Name
		owner := metav1.GetControllerOf(&p)
		_, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]
		switch {
		case p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed, mirror, owner != nil && owner.Kind == "DaemonSet":
			d.kept = append(d.kept, name)
		case owner == nil:
			d.unmanaged = append(d.unmanaged, name)
		default:
			if slices.ContainsFunc(p.Spec.Volumes, func(v corev1.Volume) bool { return v.EmptyDir != nil }) {
				emptyDir = append(emptyDir, name)
			}
			d.evicted = append(d.evicted, name)
			pending[p.UID] = p
		}
	}
	for _, names := range [][]string{d.evicted, d.kept, d.unmanaged, emptyDir} {
		slices.Sort(names)
	}
	if len(emptyDir) > 0 && !deleteEmptyDir {
		return drained{}, &drainError{connect.CodeFailedPrecondition, fmt.Errorf("pods %s have emptyDir volumes, whose data an eviction deletes; pass --delete-emptydir-data to evict them", strings.Join(emptyDir, ", "))}
	}
	// failing holds why the last eviction of a pod not evicted yet failed, and budget whether a
	// PodDisruptionBudget refused it.
	failing := map[types.UID]string{}
	budget := map[types.UID]bool{}
	for len(pending) > 0 {
		var refused []string
		for uid, p := range pending {
			if asked[uid] {
				continue
			}
			err := cs.PolicyV1().Evictions(p.Namespace).Evict(ctx, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}})
			code, refusal := evictionRefusal(err)
			switch {
			case err == nil:
				asked[uid] = true
				delete(failing, uid)
			case apierrors.IsNotFound(err):
				delete(pending, uid)
			case ctx.Err() != nil:
				// The timeout passed during the request; it is handled below.
			case refusal:
				// Refused for good: asking again or elsewhere gets the same answer.
				refused = append(refused, fmt.Sprintf("%s/%s: %v", p.Namespace, p.Name, err))
			case code != 0 || isTimeout(err):
				// A PodDisruptionBudget allows no disruption now, and another pod may become
				// ready, or the API server failed the request or did not answer in time.
				failing[uid], budget[uid] = fmt.Sprintf("%s/%s: %v", p.Namespace, p.Name, err), apierrors.IsTooManyRequests(err)
			default:
				return drained{}, fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, err)
			}
		}
		if len(refused) > 0 {
			slices.Sort(refused)
			return drained{}, &drainError{connect.CodeFailedPrecondition, fmt.Errorf("evictions refused: %s", strings.Join(refused, "; "))}
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
			var left, why []string
			blocked := false
			for uid, p := range pending {
				left = append(left, p.Namespace+"/"+p.Name)
				if f, ok := failing[uid]; ok {
					why = append(why, f)
					blocked = blocked || budget[uid]
				}
			}
			slices.Sort(left)
			slices.Sort(why)
			msg := "they did not stop"
			switch {
			case len(why) == len(left):
				msg = strings.Join(why, "; ")
			case len(why) > 0:
				msg = strings.Join(why, "; ") + "; the others did not stop"
			}
			// A budget that never allowed an eviction holds the pods wherever the drain runs.
			code := connect.CodeDeadlineExceeded
			if blocked {
				code = connect.CodeFailedPrecondition
			}
			return drained{}, &drainError{code, fmt.Errorf("pods %s still run: %s", strings.Join(left, ", "), msg)}
		case <-time.After(k.poll()):
		}
	}
	return d, nil
}

// evictionRefusal returns the HTTP status of an eviction the API server answered, and whether
// it refused it for good: a client error but too many requests, which a PodDisruptionBudget
// answers while it allows no disruption.
func evictionRefusal(err error) (int32, bool) {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return 0, false
	}
	code := status.Status().Code
	return code, code >= 400 && code < 500 && code != http.StatusTooManyRequests
}

// isTimeout reports whether a request was not answered in time.
func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
