package kubernetes

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// controlPlanePodsCurrent is the status of a control plane whose static pods run the images its
// running config names and are Ready.
const controlPlanePodsCurrent = "current"

// controlPlanePods checks, through the API server cs reaches, that the static pods of the control
// plane's kube-apiserver, kube-controller-manager and kube-scheduler on the node run the images
// the cluster names and are Ready, as their mirror pods in kube-system show it. It says what is
// not so, or controlPlanePodsCurrent. etcd is left out: its image is no version the API server's
// rollout compares, and the API server answers only while etcd serves.
//
// The kubelet replaces a static pod whose manifest changed and its mirror pod with it, so a mirror
// pod that names the new image and is Ready is the new pod's: the old one's mirror names the old
// image.
func controlPlanePods(ctx context.Context, cs kubernetes.Interface, c k8s.Cluster, node string) string {
	pods, err := cs.CoreV1().Pods(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{
		LabelSelector: "tier=control-plane",
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String(),
	})
	if err != nil {
		return fmt.Sprintf("its static pods are unknown: %v", err)
	}
	byComponent := map[string]*corev1.Pod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		// The field selector is checked again: a client that ignores it, as client-go's fake does,
		// returns every node's pods.
		if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror && p.Spec.NodeName == node {
			byComponent[p.Labels["component"]] = p
		}
	}
	for _, want := range []struct{ component, image string }{
		{"kube-apiserver", c.Images.KubeAPIServer},
		{"kube-controller-manager", c.Images.KubeControllerManager},
		{"kube-scheduler", c.Images.KubeScheduler},
	} {
		p := byComponent[want.component]
		switch {
		case p == nil:
			return want.component + " has no mirror pod yet"
		case len(p.Spec.Containers) == 0:
			return want.component + " has no container"
		case p.Spec.Containers[0].Image != want.image:
			return fmt.Sprintf("%s runs %s, not %s", want.component, p.Spec.Containers[0].Image, want.image)
		case !podReady(p):
			return want.component + " is not Ready"
		}
	}
	return controlPlanePodsCurrent
}

// podReady reports whether the pod's Ready condition is True.
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
