package kubernetes

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const clusterJSON = `{
  "kind": "controlplane",
  "endpoint": "https://10.0.0.10:6443",
  "version": "1.37.1",
  "podCIDR": "10.244.0.0/16",
  "serviceCIDR": "10.96.0.0/12",
  "dnsIP": "10.96.0.10",
  "domain": "cluster.local",
  "allowSchedulingOnControlPlanes": false,
  "extraArgs": {"kube-apiserver": {"v": "2"}},
  "images": {"etcd": "registry.k8s.io/etcd:3.7.0-0", "kubeAPIServer": "registry.k8s.io/kube-apiserver:v1.37.1",
    "kubeControllerManager": "registry.k8s.io/kube-controller-manager:v1.37.1", "kubeScheduler": "registry.k8s.io/kube-scheduler:v1.37.1"}
}`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCluster(t *testing.T) {
	c, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindControlPlane || c.ExtraArgs["kube-apiserver"]["v"] != "2" || c.Images.Etcd != "registry.k8s.io/etcd:3.7.0-0" {
		t.Errorf("cluster = %+v", c)
	}
	if host, err := c.EndpointHost(); err != nil || host != "10.0.0.10" {
		t.Errorf("EndpointHost() = %q, %v", host, err)
	}
	if ip, err := c.APIServerServiceIP(); err != nil || !ip.Equal(net.ParseIP("10.96.0.1")) {
		t.Errorf("APIServerServiceIP() = %v, %v", ip, err)
	}
}

func TestClusterValidate(t *testing.T) {
	valid, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(c *Cluster){
		"kind":         func(c *Cluster) { c.Kind = "etcd" },
		"endpoint":     func(c *Cluster) { c.Endpoint = "http://10.0.0.10:6443" },
		"service CIDR": func(c *Cluster) { c.ServiceCIDR = "10.96.0.0" },
		"pod CIDR":     func(c *Cluster) { c.PodCIDR = "" },
		"DNS IP":       func(c *Cluster) { c.DNSIP = "dns" },
		"domain":       func(c *Cluster) { c.Domain = "" },
		// These flags decide who may do what; chalkos sets them.
		"authorization-mode": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"authorization-mode": "AlwaysAllow"}}
		},
		"anonymous-auth": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"anonymous-auth": "true"}}
		},
		"authentication-config": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"authentication-config": "/tmp/a.json"}}
		},
		"enable-bootstrap-token-auth": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"enable-bootstrap-token-auth": "true"}}
		},
	} {
		c := valid
		edit(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadNode(t *testing.T) {
	n, err := ReadNode(writeFile(t, `{
	  "hostname": "cp1-host",
	  "network": {"networks": {"10-lan": {"address": ["10.0.0.11/24", "10.0.1.11/24"]}}},
	  "labels": {"zone": "a"},
	  "taints": [{"key": "dedicated", "value": "db", "effect": "NoSchedule"}],
	  "kubernetes": {"nodeName": "cp1", "nodeIP": "10.0.0.11"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "cp1" || n.Hostname != "cp1-host" || !n.IP.Equal(net.ParseIP("10.0.0.11")) || n.Labels["zone"] != "a" || len(n.Taints) != 1 {
		t.Errorf("node = %+v", n)
	}
	var ips []string
	for _, ip := range n.IPs() {
		ips = append(ips, ip.String())
	}
	if strings.Join(ips, ",") != "10.0.0.11,10.0.1.11" {
		t.Errorf("IPs() = %v", ips)
	}

	if _, err := ReadNode(writeFile(t, `{"hostname": "n1", "kubernetes": null}`)); err == nil {
		t.Error("read a node without a Kubernetes identity")
	}
	if _, err := ReadNode(writeFile(t, `{"kubernetes": {"nodeName": "n1", "nodeIP": "nope"}}`)); err == nil {
		t.Error("read a node with an invalid address")
	}
}
