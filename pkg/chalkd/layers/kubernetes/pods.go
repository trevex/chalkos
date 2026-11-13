package kubernetes

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
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
		LabelSelector: manifests.Selector(""),
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String(),
		// The API server's watch cache serves the list, which reads etcd only for a list of the
		// latest version.
		ResourceVersion: "0",
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
			byComponent[p.Labels[manifests.LabelComponent]] = p
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

// maxPodsNotRunning is how many of the pods that do not run the status lists.
const maxPodsNotRunning = 20

// podsNotRunning are the pods that do not run, as notRunning tells: the first maxPodsNotRunning
// in phaseOrder, then namespace and name order, and how many there are.
func podsNotRunning(pods []corev1.Pod) ([]*kubernetesv1.NodePod, int) {
	var list []*kubernetesv1.NodePod
	for _, p := range pods {
		if reason, ok := notRunning(p); ok {
			list = append(list, &kubernetesv1.NodePod{Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Reason: reason})
		}
	}
	slices.SortFunc(list, func(a, b *kubernetesv1.NodePod) int {
		return cmp.Or(cmp.Compare(phaseOrder(a.Phase), phaseOrder(b.Phase)), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return list[:min(len(list), maxPodsNotRunning)], len(list)
}

// phaseOrder orders the pods that do not run: pending ones first, as they wait to start, then
// running ones with a problem, then failed ones and the rest.
func phaseOrder(phase string) int {
	switch corev1.PodPhase(phase) {
	case corev1.PodPending:
		return 0
	case corev1.PodRunning:
		return 1
	case corev1.PodFailed:
		return 2
	}
	return 3
}

// notRunning reports whether a pod does not run, and why: its phase is neither Running nor
// Succeeded, or it runs with a container that waits. A failed pod of a Job runs no more, as the
// Job's record of a run, and counts as neither. The reason is containerProblem's, else the pod's
// own.
func notRunning(p corev1.Pod) (reason string, ok bool) {
	reason, waits := containerProblem(p.Status)
	switch p.Status.Phase {
	case corev1.PodSucceeded:
		return "", false
	case corev1.PodRunning:
		return reason, waits
	case corev1.PodFailed:
		if ownedByJob(p) {
			return "", false
		}
	}
	return cmp.Or(reason, p.Status.Reason), true
}

// containerProblem is the reason of the first init container that waits with a reason or ended
// with an error, prefixed Init:, as kubectl shows it, else of the first container that waits;
// waits is set when a container waits.
func containerProblem(s corev1.PodStatus) (reason string, waits bool) {
	for _, c := range s.InitContainerStatuses {
		switch {
		case c.State.Waiting != nil && c.State.Waiting.Reason != "":
			return "Init:" + c.State.Waiting.Reason, true
		case c.State.Terminated != nil && c.State.Terminated.ExitCode != 0:
			return "Init:" + cmp.Or(c.State.Terminated.Reason, "Error"), false
		}
	}
	for _, c := range s.ContainerStatuses {
		if c.State.Waiting != nil {
			return c.State.Waiting.Reason, true
		}
	}
	return "", false
}

// ownedByJob reports whether a Job owns the pod.
func ownedByJob(p corev1.Pod) bool {
	return slices.ContainsFunc(p.OwnerReferences, func(o metav1.OwnerReference) bool { return o.Kind == "Job" })
}
