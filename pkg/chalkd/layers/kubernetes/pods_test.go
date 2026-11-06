package kubernetes

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// mirrorPod is the mirror pod of a component's static pod on the node, running the image.
func mirrorPod(component, node, image string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: component + "-" + node, Namespace: metav1.NamespaceSystem,
			Labels:      map[string]string{"component": component, "tier": "control-plane"},
			Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "hash"},
		},
		Spec:   corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: component, Image: image}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

// The control plane's pods are current once each of its three components' mirror pods on the
// node names the cluster's image and is Ready; another node's pods do not count.
func TestControlPlanePods(t *testing.T) {
	c := k8s.Cluster{Images: k8s.Images{
		KubeAPIServer:         "registry.k8s.io/kube-apiserver:v1.37.2",
		KubeControllerManager: "registry.k8s.io/kube-controller-manager:v1.37.2",
		KubeScheduler:         "registry.k8s.io/kube-scheduler:v1.37.2",
	}}
	current := func() []runtime.Object {
		return []runtime.Object{
			mirrorPod("kube-apiserver", "cp1", c.Images.KubeAPIServer, true),
			mirrorPod("kube-controller-manager", "cp1", c.Images.KubeControllerManager, true),
			mirrorPod("kube-scheduler", "cp1", c.Images.KubeScheduler, true),
		}
	}
	for name, tc := range map[string]struct {
		pods []runtime.Object
		want string
	}{
		"current": {current(), "current"},
		"old image": {append(current()[:2], mirrorPod("kube-scheduler", "cp1", "registry.k8s.io/kube-scheduler:v1.37.1", true)),
			"kube-scheduler runs registry.k8s.io/kube-scheduler:v1.37.1, not registry.k8s.io/kube-scheduler:v1.37.2"},
		"not Ready": {append(current()[1:], mirrorPod("kube-apiserver", "cp1", c.Images.KubeAPIServer, false)), "kube-apiserver is not Ready"},
		"another node's": {append(current()[1:], mirrorPod("kube-apiserver", "cp2", c.Images.KubeAPIServer, true)),
			"kube-apiserver has no mirror pod yet"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := controlPlanePods(context.Background(), fake.NewClientset(tc.pods...), c, "cp1"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	if got := controlPlanePods(context.Background(), cs, c, "cp1"); got != "its static pods are unknown: connection refused" {
		t.Errorf("a failed list: %q", got)
	}
}
