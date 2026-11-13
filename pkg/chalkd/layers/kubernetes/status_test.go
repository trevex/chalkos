package kubernetes

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// TestStatusReportsTheControlPlanesState checks that a bootstrapped control plane says whether
// it runs on its current certificates.
func TestStatusReportsTheControlPlanesState(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	if err := s.k.state.MarkBootstrapped(time.Now()); err != nil {
		t.Fatal(err)
	}
	s.k.hooks.CheckControlPlane = func(context.Context, k8s.Cluster, kpki.Share) error { return nil }
	if got := kubernetestest.Status(t, s).ControlPlane; got != "current" {
		t.Errorf("control plane %q", got)
	}
}

func TestServesFile(t *testing.T) {
	served, _ := pki.SelfSigned("served", time.Now())
	other, _ := pki.SelfSigned("other", time.Now())
	pair, _ := tls.X509KeyPair([]byte(served.Certificate), []byte(served.Key))
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "served.crt"), served.Certificate)
	write(t, filepath.Join(dir, "other.crt"), other.Certificate)
	if err := servesFile(context.Background(), addr, filepath.Join(dir, "served.crt")); err != nil {
		t.Error(err)
	}
	if err := servesFile(context.Background(), addr, filepath.Join(dir, "other.crt")); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("another certificate: %v", err)
	}
	if err := servesFile(context.Background(), "127.0.0.1:1", filepath.Join(dir, "served.crt")); err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("nothing listening: %v", err)
	}
}

// withKubelet rewrites the node's cluster file to name the image's kubelet of the version, so a
// status tells it apart from the API server's 1.37.1.
func withKubelet(t *testing.T, p *Plugin, version string) {
	t.Helper()
	c, err := k8s.ReadCluster(p.k.state.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	c.Version = version
	write(t, p.k.state.Cluster, kubetest.JSON(t, c))
}

// TestStatusReportsTheVersions checks that every node reports its config's API server and its
// image's kubelet, also while it prepares, and that only a bootstrapped control plane reports its
// static pods.
func TestStatusReportsTheVersions(t *testing.T) {
	for name, tc := range map[string]struct {
		kind  string
		share bool
	}{
		"preparing worker":        {k8s.KindWorker, false},
		"joined worker":           {k8s.KindWorker, true},
		"preparing control plane": {k8s.KindControlPlane, false},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := kubernetesNode(t, tc.kind, tc.share)
			withKubelet(t, s, "1.36.4")
			st := kubernetestest.Status(t, s)
			if st.ApiServerVersion != "1.37.1" || st.KubeletVersion != "1.36.4" {
				t.Errorf("API server %q, kubelet %q; want 1.37.1 and 1.36.4", st.ApiServerVersion, st.KubeletVersion)
			}
			if st.ControlPlanePods != "" || st.ControlPlanePodsCurrent {
				t.Errorf("control-plane pods %q, %v; want none", st.ControlPlanePods, st.ControlPlanePodsCurrent)
			}
		})
	}
}

// TestStatusReportsTheStaticPods checks that a bootstrapped control plane reports whether its
// static pods run its config's images, through its own API server.
func TestStatusReportsTheStaticPods(t *testing.T) {
	images := k8s.Images{
		KubeAPIServer:         "registry.k8s.io/kube-apiserver:v1.37.1",
		KubeControllerManager: "registry.k8s.io/kube-controller-manager:v1.37.1",
		KubeScheduler:         "registry.k8s.io/kube-scheduler:v1.37.1",
	}
	for name, tc := range map[string]struct {
		client  func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error)
		want    string
		current bool
	}{
		"current": {func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
			return fake.NewClientset(
				mirrorPod("kube-apiserver", "n1", images.KubeAPIServer, true),
				mirrorPod("kube-controller-manager", "n1", images.KubeControllerManager, true),
				mirrorPod("kube-scheduler", "n1", images.KubeScheduler, true),
			), nil
		}, "current", true},
		"not Ready": {func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
			return fake.NewClientset(
				mirrorPod("kube-apiserver", "n1", images.KubeAPIServer, false),
				mirrorPod("kube-controller-manager", "n1", images.KubeControllerManager, true),
				mirrorPod("kube-scheduler", "n1", images.KubeScheduler, true),
			), nil
		}, "kube-apiserver is not Ready", false},
		"no client": {func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
			return nil, errors.New("no share")
		}, "its static pods are unknown: no share", false},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
			if err := s.k.state.MarkBootstrapped(time.Now()); err != nil {
				t.Fatal(err)
			}
			s.k.hooks.Node = func(context.Context) (*corev1.Node, error) {
				n := kubernetestest.ReadyNode()
				n.Name = "n1"
				return n, nil
			}
			s.k.hooks.APIClient = tc.client
			st := kubernetestest.Status(t, s)
			if st.ControlPlanePods != tc.want || st.ControlPlanePodsCurrent != tc.current {
				t.Errorf("control-plane pods %q, %v; want %q, %v", st.ControlPlanePods, st.ControlPlanePodsCurrent, tc.want, tc.current)
			}
			if st.ApiServerVersion != "1.37.1" || st.KubeletVersion != "1.37.1" {
				t.Errorf("API server %q, kubelet %q", st.ApiServerVersion, st.KubeletVersion)
			}
		})
	}
}

// TestStatusReportsWhyTheNodeIsNotReady checks that the status carries the reason and the message
// of the Node's Ready condition, as the kubelet gives them on a cluster without a CNI.
func TestStatusReportsWhyTheNodeIsNotReady(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindWorker, true)
	const message = "container runtime network not ready: NetworkReady=false reason:NetworkPluginNotReady message:Network plugin returns error: cni plugin not initialized"
	s.k.hooks.Node = func(context.Context) (*corev1.Node, error) {
		n := kubernetestest.ReadyNode()
		n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady", Message: message}}
		return n, nil
	}
	st := kubernetestest.Status(t, s)
	if st.NodeReady != "False" || st.NodeReadyReason != "KubeletNotReady" || st.NodeReadyMessage != message {
		t.Errorf("node ready %q, reason %q, message %q", st.NodeReady, st.NodeReadyReason, st.NodeReadyMessage)
	}
}

// TestStatusReportsPodsNotRunning lists the node's pods that do not run, read with the kubelet's
// credentials: those neither running nor succeeded, other than a Job's failed ones, and running
// ones with a waiting container, pending ones first.
func TestStatusReportsPodsNotRunning(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindWorker, true)
	waiting := func(reason string) []corev1.ContainerStatus {
		return []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}}
	}
	s.k.hooks.Node = func(context.Context) (*corev1.Node, error) {
		n := kubernetestest.ReadyNode()
		n.Name = "n1"
		return n, nil
	}
	job := []metav1.OwnerReference{{Kind: "Job", Name: "backup"}}
	s.k.hooks.Pods = func(_ context.Context, node string) ([]corev1.Pod, error) {
		if node != "n1" {
			t.Errorf("pods of %q", node)
		}
		return []corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "ok"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "done"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "crash"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: waiting("CrashLoopBackOff")}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "pull"}, Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: waiting("ImagePullBackOff")}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "evicted"}, Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "backup-1", OwnerReferences: job}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "init"}, Status: corev1.PodStatus{
				Phase:                 corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}}},
				ContainerStatuses:     waiting("PodInitializing"),
			}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "sidecar"}, Status: corev1.PodStatus{
				Phase:                 corev1.PodRunning,
				InitContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
			}},
		}, nil
	}
	st := kubernetestest.Status(t, s)
	var got []string
	for _, p := range st.PodsNotRunning {
		got = append(got, p.Namespace+"/"+p.Name+" "+p.Phase+" "+p.Reason)
	}
	want := []string{
		"apps/pull Pending ImagePullBackOff", "web/init Pending Init:Error",
		"web/crash Running CrashLoopBackOff", "web/sidecar Running Init:CrashLoopBackOff",
		"apps/evicted Failed Evicted",
	}
	if !slices.Equal(got, want) || st.PodsNotRunningTotal != 5 || st.PodsError != "" {
		t.Errorf("pods not running %q of %d, error %q; want %q", got, st.PodsNotRunningTotal, st.PodsError, want)
	}

	s.k.hooks.Pods = func(context.Context, string) ([]corev1.Pod, error) { return nil, errors.New("forbidden") }
	if st := kubernetestest.Status(t, s); st.PodsError != "forbidden" || len(st.PodsNotRunning) != 0 {
		t.Errorf("pods error %q, pods %v", st.PodsError, st.PodsNotRunning)
	}
}

// TestStatusListsTwentyPodsNotRunning lists the first 20 of the pods that do not run, and counts
// them all.
func TestStatusListsTwentyPodsNotRunning(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindWorker, true)
	s.k.hooks.Pods = func(context.Context, string) ([]corev1.Pod, error) {
		var pods []corev1.Pod
		for i := range 25 {
			pods = append(pods, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: fmt.Sprintf("pod-%02d", i)}, Status: corev1.PodStatus{Phase: corev1.PodPending}})
		}
		return pods, nil
	}
	st := kubernetestest.Status(t, s)
	if len(st.PodsNotRunning) != 20 || st.PodsNotRunningTotal != 25 || st.PodsNotRunning[19].Name != "pod-19" {
		t.Errorf("%d pods listed of %d", len(st.PodsNotRunning), st.PodsNotRunningTotal)
	}
}
