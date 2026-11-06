package kubernetes

import (
	"strings"
	"testing"
)

// skewCluster is clusterJSON's cluster with a kubelet of the version and the API server,
// controller-manager, scheduler and kube-proxy of the versions given.
func skewCluster(t *testing.T, kubelet, api, cm, sched, proxy string) Cluster {
	t.Helper()
	c, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	c.Version = kubelet
	c.Images.KubeAPIServer = "registry.k8s.io/kube-apiserver:" + api
	c.Images.KubeControllerManager = "registry.k8s.io/kube-controller-manager:" + cm
	c.Images.KubeScheduler = "registry.k8s.io/kube-scheduler:" + sched
	c.Images.KubeProxy = "registry.k8s.io/kube-proxy:" + proxy
	return c
}

// Kubernetes's version skew policy, at each boundary.
func TestClusterVersionSkew(t *testing.T) {
	for name, tc := range map[string]struct {
		kubelet, api, cm, sched, proxy string
		want                           string
	}{
		"all alike":                       {"1.37.1", "v1.37.1", "v1.37.1", "v1.37.1", "v1.37.1", ""},
		"patches differ":                  {"1.37.5", "v1.37.1", "v1.37.0", "v1.37.9", "v1.37.2", ""},
		"API server a suffix":             {"1.37.1", "v1.38.0-rc.1", "v1.38.0-rc.1", "v1.37.1", "v1.37.1", ""},
		"API server three minors newer":   {"1.37.1", "v1.40.0", "v1.40.0", "v1.39.0", "v1.40.0", ""},
		"API server four minors newer":    {"1.37.1", "v1.41.0", "v1.41.0", "v1.41.0", "v1.40.0", "images.apiServer: v1.41.0 is 4 minors newer than the image's kubelet 1.37"},
		"API server older than kubelet":   {"1.37.1", "v1.36.9", "v1.36.9", "v1.36.9", "v1.36.9", "images.apiServer: v1.36.9 is older than the image's kubelet 1.37"},
		"controller-manager newer":        {"1.37.1", "v1.37.1", "v1.38.0", "v1.37.1", "v1.37.1", "images.controllerManager: v1.38.0 is newer than images.apiServer v1.37.1"},
		"controller-manager one older":    {"1.36.0", "v1.37.1", "v1.36.0", "v1.37.1", "v1.37.1", ""},
		"controller-manager two older":    {"1.35.0", "v1.37.1", "v1.35.0", "v1.37.1", "v1.37.1", "images.controllerManager: v1.35.0 is 2 minors older than images.apiServer v1.37.1"},
		"scheduler newer":                 {"1.37.1", "v1.37.1", "v1.37.1", "v1.38.0", "v1.37.1", "images.scheduler: v1.38.0 is newer than images.apiServer"},
		"scheduler two older":             {"1.35.0", "v1.37.1", "v1.37.1", "v1.35.0", "v1.37.1", "images.scheduler: v1.35.0 is 2 minors older"},
		"proxy newer than API server":     {"1.37.1", "v1.37.1", "v1.37.1", "v1.37.1", "v1.38.0", "images.proxy: v1.38.0 is newer than images.apiServer v1.37.1"},
		"proxy three older than API":      {"1.37.0", "v1.40.0", "v1.40.0", "v1.40.0", "v1.37.0", ""},
		"proxy four older than API":       {"1.37.0", "v1.40.0", "v1.40.0", "v1.40.0", "v1.36.0", "images.proxy: v1.36.0 is 4 minors older than images.apiServer v1.40.0"},
		"proxy three newer than kubelet":  {"1.37.0", "v1.40.0", "v1.40.0", "v1.40.0", "v1.40.0", ""},
		"proxy three older than kubelet":  {"1.40.0", "v1.40.0", "v1.40.0", "v1.40.0", "v1.37.0", ""},
		"another major":                   {"1.37.1", "v2.37.1", "v2.37.1", "v2.37.1", "v2.37.1", "images.apiServer: v2.37.1 is of Kubernetes 2, the image's kubelet v1.37.1 of Kubernetes 1"},
		"proxy of another major":          {"1.37.1", "v1.37.1", "v1.37.1", "v1.37.1", "v2.37.1", "images.proxy: v2.37.1 is of Kubernetes 2"},
		"API server without a version":    {"1.37.1", "latest", "v1.37.1", "v1.37.1", "v1.37.1", "images.apiServer: registry.k8s.io/kube-apiserver:latest: its tag is no version"},
		"proxy with a version of its own": {"1.37.1", "v1.37.1", "v1.37.1", "v1.37.1", "1.37.1", "images.proxy: registry.k8s.io/kube-proxy:1.37.1: its tag is no version"},
	} {
		t.Run(name, func(t *testing.T) {
			err := skewCluster(t, tc.kubelet, tc.api, tc.cm, tc.sched, tc.proxy).Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// A reference by digest alone carries no version; etcd, CoreDNS and the pause image need a
// reference alone, and flannel's image goes with flannel.
func TestClusterImages(t *testing.T) {
	const digest = "@sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d"
	for name, tc := range map[string]struct {
		change func(c *Cluster)
		want   string
	}{
		"pinned":           {func(c *Cluster) { c.Images.KubeAPIServer += digest }, ""},
		"by digest alone":  {func(c *Cluster) { c.Images.KubeAPIServer = "registry.k8s.io/kube-apiserver" + digest }, "images.apiServer: registry.k8s.io/kube-apiserver" + digest + ": its tag is no version"},
		"etcd by digest":   {func(c *Cluster) { c.Images.Etcd = "registry.k8s.io/etcd" + digest }, ""},
		"no pause":         {func(c *Cluster) { c.Images.Pause = "" }, "images.pause: no image"},
		"bad CoreDNS":      {func(c *Cluster) { c.Images.CoreDNS = "coredns:v1" }, "images.coredns: "},
		"flannel":          {func(c *Cluster) { c.Flannel, c.Images.Flannel = &Flannel{}, "ghcr.io/flannel-io/flannel:v0.28.9" }, ""},
		"flannel no image": {func(c *Cluster) { c.Flannel = &Flannel{} }, "images.flannel: no image"},
		"image no flannel": {func(c *Cluster) { c.Images.Flannel = "ghcr.io/flannel-io/flannel:v0.28.9" }, "images.flannel: an image, but the cluster runs no flannel"},
		"bad flannel":      {func(c *Cluster) { c.Flannel, c.Images.Flannel = &Flannel{}, "flannel:v0.28.9" }, "images.flannel: "},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := ReadCluster(writeFile(t, clusterJSON))
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&c)
			err = c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
