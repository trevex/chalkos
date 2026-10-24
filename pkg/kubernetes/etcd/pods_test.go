package etcd

import (
	"context"
	"net/netip"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func etcdPod(name, ip string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system", Labels: labels},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

// The etcd static pods other than the node's own give the endpoints, sorted.
func TestEndpointsFromPods(t *testing.T) {
	etcd := map[string]string{"component": "etcd", "tier": "control-plane"}
	client := fake.NewClientset(
		etcdPod("etcd-cp3", "10.0.0.3", etcd),
		etcdPod("etcd-cp1", "10.0.0.1", etcd),
		etcdPod("etcd-cp2", "10.0.0.2", etcd),
		etcdPod("etcd-pending", "", etcd),
		etcdPod("kube-apiserver-cp1", "10.0.0.9", map[string]string{"component": "kube-apiserver", "tier": "control-plane"}),
	)
	got, err := EndpointsFromPods(context.Background(), client, []netip.Addr{netip.MustParseAddr("10.0.0.2")})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://10.0.0.1:2379", "https://10.0.0.3:2379"}; !slices.Equal(got, want) {
		t.Errorf("endpoints %v, want %v", got, want)
	}
	if _, err := EndpointsFromPods(context.Background(), client, []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")}); err == nil {
		t.Error("found endpoints with only the node's own etcd pods listed")
	}
}
