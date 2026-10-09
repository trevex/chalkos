package chalkd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

func pod(namespace, name, node string, owner string, mutate ...func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(namespace + "-" + name)},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if owner != "" {
		controller := true
		p.OwnerReferences = []metav1.OwnerReference{{Kind: owner, Name: "owner", Controller: &controller}}
	}
	for _, m := range mutate {
		m(p)
	}
	return p
}

// drainLab is a bootstrapped control plane whose API server is a fake holding w1's pods. An
// eviction of a pod named guarded breaks its PodDisruptionBudget the first refusals times.
type drainLab struct {
	s        *Server
	cs       *fake.Clientset
	mu       sync.Mutex
	refusals int
	evicted  []string
}

func newDrainLab(t *testing.T, refusals int, nodes ...*corev1.Node) *drainLab {
	t.Helper()
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	write(t, s.Kubernetes.Paths.Bootstrapped(), "")
	objects := []runtime.Object{
		pod("default", "web-1", "w1", "ReplicaSet"),
		pod("default", "guarded", "w1", "StatefulSet"),
		pod("default", "bare", "w1", ""),
		pod("default", "done", "w1", "Job", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
		pod("kube-flannel", "flannel", "w1", "DaemonSet"),
		pod("kube-system", "static-w1", "w1", "Node", func(p *corev1.Pod) {
			p.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "x"}
		}),
		pod("default", "web-2", "w2", "ReplicaSet"),
	}
	for _, n := range nodes {
		objects = append(objects, n)
	}
	l := &drainLab{s: s, cs: fake.NewClientset(objects...), refusals: refusals}
	l.cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ev := action.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction)
		l.mu.Lock()
		defer l.mu.Unlock()
		if ev.Name == "guarded" && l.refusals > 0 {
			l.refusals--
			return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)
		}
		l.evicted = append(l.evicted, ev.Namespace+"/"+ev.Name)
		return true, nil, l.cs.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), ev.Namespace, ev.Name)
	})
	s.Kubernetes.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return l.cs, nil }
	s.Kubernetes.RotationPoll = 10 * time.Millisecond
	return l
}

func (l *drainLab) drain(timeout uint32) (*nodev1.DrainNodeResponse, error) {
	resp, err := l.s.DrainNode(context.Background(), connect.NewRequest(&nodev1.DrainNodeRequest{Node: "w1", TimeoutSeconds: timeout}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (l *drainLab) uncordon() bool {
	resp, err := l.s.UncordonNode(context.Background(), connect.NewRequest(&nodev1.UncordonNodeRequest{Node: "w1"}))
	if err != nil {
		panic(err)
	}
	return resp.Msg.Uncordoned
}

func (l *drainLab) node(t *testing.T) *corev1.Node {
	t.Helper()
	n, err := l.cs.CoreV1().Nodes().Get(context.Background(), "w1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (l *drainLab) pods(t *testing.T) []string {
	t.Helper()
	list, err := l.cs.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list.Items {
		names = append(names, p.Namespace+"/"+p.Name)
	}
	slices.Sort(names)
	return names
}

func w1(unschedulable bool) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}, Spec: corev1.NodeSpec{Unschedulable: unschedulable}}
}

// TestDrainNode cordons and marks w1, evicts the pods of controllers but DaemonSets, retrying an
// eviction its PodDisruptionBudget refuses, and leaves the rest; draining again changes nothing.
// Uncordoning makes it schedulable again, once.
func TestDrainNode(t *testing.T) {
	l := newDrainLab(t, 2, w1(false))
	got, err := l.drain(0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Marked || !slices.Equal(got.Evicted, []string{"default/guarded", "default/web-1"}) ||
		!slices.Equal(got.Kept, []string{"default/done", "kube-flannel/flannel", "kube-system/static-w1"}) || !slices.Equal(got.Unmanaged, []string{"default/bare"}) {
		t.Errorf("drain = %+v", got)
	}
	if n := l.node(t); !n.Spec.Unschedulable || n.Annotations[UpgradeCordon] != "true" {
		t.Errorf("w1 is not cordoned and marked: %+v", n)
	}
	if want := []string{"default/bare", "default/done", "default/web-2", "kube-flannel/flannel", "kube-system/static-w1"}; !slices.Equal(l.pods(t), want) {
		t.Errorf("pods %v, want %v", l.pods(t), want)
	}
	if l.refusals != 0 {
		t.Errorf("the budget refused %d evictions less than it was asked", l.refusals)
	}
	if again, err := l.drain(0); err != nil || !again.Marked || len(again.Evicted) != 0 {
		t.Errorf("drain again = %+v, %v", again, err)
	}
	if !l.uncordon() {
		t.Error("w1 was not uncordoned")
	}
	if n := l.node(t); n.Spec.Unschedulable || n.Annotations[UpgradeCordon] != "" {
		t.Errorf("w1 stays cordoned: %+v", n)
	}
	if l.uncordon() {
		t.Error("w1 was uncordoned twice")
	}
}

// TestDrainKeepsAnOperatorsCordon leaves a node cordoned before unmarked, and cordoned.
func TestDrainKeepsAnOperatorsCordon(t *testing.T) {
	l := newDrainLab(t, 0, w1(true))
	got, err := l.drain(0)
	if err != nil || got.Marked {
		t.Fatalf("drain = %+v, %v", got, err)
	}
	if l.uncordon() || !l.node(t).Spec.Unschedulable {
		t.Error("the upgrade uncordoned a node an operator cordoned")
	}
}

// TestDrainWaitsForTheBudget gives up when a PodDisruptionBudget never allows the eviction,
// naming the pod and why, and leaves the node cordoned. Another control plane would not do
// better, so this is no timeout.
func TestDrainWaitsForTheBudget(t *testing.T) {
	l := newDrainLab(t, 1000, w1(false))
	_, err := l.drain(1)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "pods default/guarded still run") || !strings.Contains(err.Error(), "disruption budget") {
		t.Errorf("drain = %v", err)
	}
	if !l.node(t).Spec.Unschedulable {
		t.Error("w1 is not cordoned")
	}
	for _, e := range l.evicted {
		if e == "default/guarded" {
			t.Error("the guarded pod was evicted")
		}
	}
}

func TestDrainRefusals(t *testing.T) {
	l := newDrainLab(t, 0)
	if _, err := l.drain(0); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("draining an unknown node: %v", err)
	}
	s, _ := kubernetesServer(t, k8s.KindWorker, true)
	if _, err := s.DrainNode(context.Background(), connect.NewRequest(&nodev1.DrainNodeRequest{Node: "w1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("draining through a worker: %v", err)
	}
}

// TestDrainRefused stops at evictions the API server refuses for good, naming each pod and why,
// and leaves the node cordoned; another control plane would be refused the same.
func TestDrainRefused(t *testing.T) {
	l := newDrainLab(t, 0, w1(false))
	l.cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		switch action.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction).Name {
		case "web-1":
			return true, nil, apierrors.NewForbidden(policyv1.Resource("evictions"), "web-1", errors.New("not allowed"))
		case "guarded":
			return true, nil, apierrors.NewInternalError(errors.New("This pod has more than one PodDisruptionBudget, which the eviction subresource does not support."))
		}
		return false, nil, nil
	})
	_, err := l.drain(5)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "default/web-1: ") || !strings.Contains(err.Error(), "not allowed") ||
		!strings.Contains(err.Error(), "default/guarded: ") || !strings.Contains(err.Error(), "more than one PodDisruptionBudget") || !strings.Contains(err.Error(), "stays cordoned") {
		t.Errorf("drain = %v", err)
	}
	if !l.node(t).Spec.Unschedulable {
		t.Error("w1 is not cordoned")
	}
}

// TestDrainTimesOut gives up on pods that were evicted but never stopped: the one case of a
// timeout.
func TestDrainTimesOut(t *testing.T) {
	l := newDrainLab(t, 0, w1(false))
	l.cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		// Accepted, but the pod keeps running.
		return action.GetSubresource() == "eviction", nil, nil
	})
	_, err := l.drain(1)
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded || !strings.Contains(err.Error(), "pods default/guarded, default/web-1 still run: they did not stop") {
		t.Errorf("drain = %v", err)
	}
}

// TestDrainCordonsAMarkedNode cordons a node that carries the upgrade's mark but was made
// schedulable by hand, before it evicts anything.
func TestDrainCordonsAMarkedNode(t *testing.T) {
	marked := w1(false)
	marked.Annotations = map[string]string{UpgradeCordon: "true"}
	l := newDrainLab(t, 0, marked)
	l.cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		// The clientset is locked while its reactors run; its tracker is not.
		n, err := l.cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("nodes"), "", "w1")
		if err != nil || !n.(*corev1.Node).Spec.Unschedulable {
			t.Errorf("a pod was evicted from a schedulable node: %v", err)
		}
		return false, nil, nil
	})
	got, err := l.drain(0)
	if err != nil || !got.Marked {
		t.Fatalf("drain = %+v, %v", got, err)
	}
	if n := l.node(t); !n.Spec.Unschedulable || n.Annotations[UpgradeCordon] != "true" {
		t.Errorf("w1 is not cordoned and marked: %+v", n)
	}
}

// TestDrainEmptyDir evicts pods with emptyDir volumes only when their data may be lost, and
// names them otherwise before evicting anything.
func TestDrainEmptyDir(t *testing.T) {
	cache := pod("default", "cache", "w1", "ReplicaSet", func(p *corev1.Pod) {
		p.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	})
	l := newDrainLab(t, 0, w1(false))
	if err := l.cs.Tracker().Add(cache); err != nil {
		t.Fatal(err)
	}
	_, err := l.drain(0)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "default/cache") || !strings.Contains(err.Error(), "--delete-emptydir-data") {
		t.Errorf("drain = %v", err)
	}
	if len(l.evicted) != 0 || !l.node(t).Spec.Unschedulable {
		t.Errorf("evicted %v, cordoned %v", l.evicted, l.node(t).Spec.Unschedulable)
	}
	resp, err := l.s.DrainNode(context.Background(), connect.NewRequest(&nodev1.DrainNodeRequest{Node: "w1", DeleteEmptydirData: true}))
	if err != nil || !slices.Contains(resp.Msg.Evicted, "default/cache") {
		t.Errorf("drain deleting emptyDir data = %v, %v", resp, err)
	}
}

// TestDrainLeavesTerminatingPods neither evicts nor waits for pods that terminate already.
func TestDrainLeavesTerminatingPods(t *testing.T) {
	leaving := pod("default", "leaving", "w1", "ReplicaSet", func(p *corev1.Pod) {
		now := metav1.Now()
		p.DeletionTimestamp = &now
	})
	l := newDrainLab(t, 0, w1(false))
	if err := l.cs.Tracker().Add(leaving); err != nil {
		t.Fatal(err)
	}
	got, err := l.drain(1)
	if err != nil || slices.Contains(got.Evicted, "default/leaving") || slices.Contains(l.evicted, "default/leaving") {
		t.Errorf("drain = %+v, %v; evicted %v", got, err, l.evicted)
	}
}
