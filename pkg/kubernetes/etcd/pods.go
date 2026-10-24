package etcd

import (
	"context"
	"errors"
	"net"
	"slices"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EndpointsFromPods lists the client URLs of the etcd members not at an address in self, from the
// mirror pods of etcd's static pods the API server has. The URLs are sorted; finding none is an
// error.
func EndpointsFromPods(ctx context.Context, client kubernetes.Interface, self []net.IP) ([]string, error) {
	pods, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "component=etcd,tier=control-plane"})
	if err != nil {
		return nil, err
	}
	var endpoints []string
	for _, pod := range pods.Items {
		ip := net.ParseIP(pod.Status.PodIP)
		if ip == nil || slices.ContainsFunc(self, ip.Equal) {
			continue
		}
		endpoints = append(endpoints, ClientURL(ip))
	}
	if len(endpoints) == 0 {
		return nil, errors.New("the cluster lists no etcd members")
	}
	sort.Strings(endpoints)
	return endpoints, nil
}
