package kubernetes

import (
	"strings"
	"testing"
)

const imageJSON = `{
  "version": "1.37.1",
  "cni": "flannel",
  "images": {"etcd": "registry.k8s.io/etcd:3.7.0-0", "kubeAPIServer": "registry.k8s.io/kube-apiserver:v1.37.1",
    "kubeControllerManager": "registry.k8s.io/kube-controller-manager:v1.37.1", "kubeScheduler": "registry.k8s.io/kube-scheduler:v1.37.1",
    "kubeProxy": "registry.k8s.io/kube-proxy:v1.37.1", "coredns": "registry.k8s.io/coredns/coredns:v1.14.6",
    "flannel": "ghcr.io/flannel-io/flannel:v0.28.9"}
}`

func TestReadImage(t *testing.T) {
	img, err := ReadImage(writeFile(t, imageJSON))
	if err != nil {
		t.Fatal(err)
	}
	if img.Version != "1.37.1" || img.CNI != CNIFlannel || img.Images.KubeProxy != "registry.k8s.io/kube-proxy:v1.37.1" || img.Images.CoreDNS == "" {
		t.Errorf("image = %+v", img)
	}
}

func TestParseImageRejects(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"no version":            {`"version": "1.37.1"`, `"version": ""`, "no Kubernetes version"},
		"unknown CNI":           {`"cni": "flannel"`, `"cni": "calico"`, `unknown CNI provider "calico"`},
		"flannel without image": {`"flannel": "ghcr.io/flannel-io/flannel:v0.28.9"`, `"flannel": ""`, "no flannel image"},
		"none with flannel":     {`"cni": "flannel"`, `"cni": "none"`, "a flannel image, but its CNI provider is none"},
		"no etcd image":         {`"etcd": "registry.k8s.io/etcd:3.7.0-0"`, `"etcd": ""`, "no etcd image"},
		"no API server image":   {`"kubeAPIServer": "registry.k8s.io/kube-apiserver:v1.37.1"`, `"kubeAPIServer": ""`, "no kubeAPIServer image"},
		"no controller-manager image": {`"kubeControllerManager": "registry.k8s.io/kube-controller-manager:v1.37.1"`, `"kubeControllerManager": ""`,
			"no kubeControllerManager image"},
		"no scheduler image":  {`"kubeScheduler": "registry.k8s.io/kube-scheduler:v1.37.1"`, `"kubeScheduler": ""`, "no kubeScheduler image"},
		"no kube-proxy image": {`"kubeProxy": "registry.k8s.io/kube-proxy:v1.37.1"`, `"kubeProxy": ""`, "no kubeProxy image"},
		"no CoreDNS image":    {`"coredns": "registry.k8s.io/coredns/coredns:v1.14.6"`, `"coredns": ""`, "no coredns image"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseImage([]byte(strings.Replace(imageJSON, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
