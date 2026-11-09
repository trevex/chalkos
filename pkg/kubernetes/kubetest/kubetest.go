// Package kubetest holds the fixtures of the tests of a node's Kubernetes cluster: a cluster and
// its images, a node, and the Cluster and Node documents of a config that describe them.
package kubetest

import (
	"encoding/json"
	"maps"
	"net/netip"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// Images are the images of a cluster without flannel.
var Images = kubernetes.Images{
	Etcd:                  "registry.k8s.io/etcd:3.7.0-0",
	KubeAPIServer:         "registry.k8s.io/kube-apiserver:v1.37.1",
	KubeControllerManager: "registry.k8s.io/kube-controller-manager:v1.37.1",
	KubeScheduler:         "registry.k8s.io/kube-scheduler:v1.37.1",
	KubeProxy:             "registry.k8s.io/kube-proxy:v1.37.1",
	CoreDNS:               "registry.k8s.io/coredns/coredns:v1.14.6",
	Pause:                 "registry.k8s.io/pause:3.10.2",
}

// FlannelImage is the image of a cluster with flannel.
const FlannelImage = "ghcr.io/flannel-io/flannel:v0.28.9"

// PodCIDRs, ServiceCIDRs and DNSIPs are the ranges and the cluster DNS's addresses of Cluster.
var (
	PodCIDRs     = kubernetes.ByFamily[netip.Prefix]{IPv4: netip.MustParsePrefix("10.244.0.0/16"), IPv6: netip.MustParsePrefix("fd00:10:244::/56")}
	ServiceCIDRs = kubernetes.ByFamily[netip.Prefix]{IPv4: netip.MustParsePrefix("10.96.0.0/12"), IPv6: netip.MustParsePrefix("fd00:10:96::/112")}
	DNSIPs       = kubernetes.ByFamily[netip.Addr]{IPv4: netip.MustParseAddr("10.96.0.10"), IPv6: netip.MustParseAddr("fd00:10:96::a")}
)

// Cluster is a cluster of the kind at https://10.0.0.10:6443 on Kubernetes 1.37.1, with the
// default ranges of both families, the domain cluster.local and Images, changed by the edits in
// order.
func Cluster(kind string, edits ...func(c *kubernetes.Cluster)) kubernetes.Cluster {
	c := kubernetes.Cluster{
		Kind:              kind,
		Endpoint:          "https://10.0.0.10:6443",
		Version:           "1.37.1",
		APIServerVersion:  "1.37.1",
		Domain:            "cluster.local",
		PodCIDRs:          PodCIDRs,
		ServiceCIDRs:      ServiceCIDRs,
		DNSIPs:            DNSIPs,
		NodeCIDRMaskSizes: kubernetes.ByFamily[int]{IPv4: 24, IPv6: 64},
		Images:            Images,
	}
	for _, edit := range edits {
		edit(&c)
	}
	return c
}

// IPv4Only drops the IPv6 ranges, address and node mask size of c.
func IPv4Only(c *kubernetes.Cluster) {
	c.PodCIDRs.IPv6 = netip.Prefix{}
	c.ServiceCIDRs.IPv6 = netip.Prefix{}
	c.DNSIPs.IPv6 = netip.Addr{}
	c.NodeCIDRMaskSizes.IPv6 = 0
}

// JSON is c as a cluster file holds it.
func JSON(t testing.TB, c kubernetes.Cluster) string {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Node is the node called name with the addresses ips.
func Node(name string, ips ...string) kubernetes.Node {
	n := kubernetes.Node{Name: name}
	for _, ip := range ips {
		n.IPs = append(n.IPs, netip.MustParseAddr(ip))
	}
	return n
}

// DocumentImages are the images ClusterDoc names, by the fields of a Cluster document: those of
// a flannel image, as the test schemas are.
var DocumentImages = map[string]string{
	"apiServer":         "registry.k8s.io/kube-apiserver:v1.37.1",
	"controllerManager": "registry.k8s.io/kube-controller-manager:v1.37.1",
	"scheduler":         "registry.k8s.io/kube-scheduler:v1.37.1",
	"proxy":             "registry.k8s.io/kube-proxy:v1.37.1",
	"etcd":              "registry.k8s.io/etcd:3.7.0-0",
	"coredns":           "registry.k8s.io/coredns/coredns:v1.14.6",
	"pause":             "registry.k8s.io/pause:3.10.2",
	"flannel":           FlannelImage,
}

// NodeDoc is a Kubernetes Node document called name with the spec given as YAML.
func NodeDoc(name, spec string) string {
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

// ClusterDoc is a Kubernetes Cluster document called name with the endpoint
// https://10.0.0.10:6443, DocumentImages unless spec names images, and the fields the YAML
// mapping spec gives.
func ClusterDoc(name, spec string) string {
	fields := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(spec), "{"), "}"))
	if !strings.Contains(fields, "images:") {
		b, _ := json.Marshal(DocumentImages)
		fields = strings.TrimSuffix("images: "+string(b)+", "+fields, ", ")
	}
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: " + name + "}\n" +
		`spec: {endpoint: "https://10.0.0.10:6443", ` + fields + "}\n"
}

// DocumentImagesWith is DocumentImages with the changes given, as a YAML flow mapping; a field
// given as "" is left out.
func DocumentImagesWith(t testing.TB, changes map[string]string) string {
	t.Helper()
	m := maps.Clone(DocumentImages)
	for k, v := range changes {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
