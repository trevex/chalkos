package etcd

import (
	"context"
	"errors"
	"net/netip"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
)

// EndpointsFromPods lists the client URLs of the etcd members not at an address in self, from the
// mirror pods of etcd's static pods the API server has. The URLs are sorted; finding none is an
// error.
func EndpointsFromPods(ctx context.Context, client kubernetes.Interface, self []netip.Addr) ([]string, error) {
	pods, err := client.CoreV1().Pods(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{LabelSelector: manifests.Selector("etcd")})
	if err != nil {
		return nil, err
	}
	var endpoints []string
	for _, pod := range pods.Items {
		ip, err := netip.ParseAddr(pod.Status.PodIP)
		if err != nil || slices.Contains(self, ip.Unmap()) {
			continue
		}
		endpoints = append(endpoints, k8s.EtcdClientURL(ip.Unmap()))
	}
	if len(endpoints) == 0 {
		return nil, errors.New("the cluster lists no etcd members")
	}
	slices.Sort(endpoints)
	return endpoints, nil
}
