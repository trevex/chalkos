package addons

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

func testCluster(families ...string) kubernetes.Cluster {
	return kubernetes.Cluster{
		Kind: kubernetes.KindControlPlane, Endpoint: "https://10.0.0.10:6443", Version: "1.37.1", Domain: "lab.local",
		IPFamilies:   families,
		PodCIDRs:     kubernetes.ByFamily[string]{IPv4: "10.244.0.0/16", IPv6: "fd00:10:244::/56"},
		ServiceCIDRs: kubernetes.ByFamily[string]{IPv4: "10.96.0.0/12", IPv6: "fd00:10:96::/112"},
		DNSIPs:       kubernetes.ByFamily[string]{IPv4: "10.96.0.10", IPv6: "fd00:10:96::a"},
		Images:       kubernetes.Images{KubeProxy: "proxy", CoreDNS: "coredns", Flannel: "flannel"},
		Flannel:      &kubernetes.Flannel{MTU: 1430},
	}
}

// find returns the object of the kind and name, or fails the test.
func find(t *testing.T, objects []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, o := range objects {
		if o["kind"] == kind && o["metadata"].(map[string]any)["name"] == name {
			return o
		}
	}
	t.Fatalf("no %s %s", kind, name)
	return nil
}

func data(t *testing.T, o map[string]any, key string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(o["data"].(map[string]any)[key].(string)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderOrder(t *testing.T) {
	objects, err := Render(testCluster("ipv4"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, o := range objects {
		kinds = append(kinds, o["kind"].(string))
	}
	// RBAC, kube-proxy, flannel, CoreDNS.
	want := "ClusterRoleBinding ClusterRoleBinding ClusterRoleBinding ServiceAccount ClusterRoleBinding ConfigMap DaemonSet " +
		"Namespace ClusterRole ClusterRoleBinding ServiceAccount ConfigMap DaemonSet " +
		"ServiceAccount ClusterRole ClusterRoleBinding ConfigMap Deployment Service"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("kinds =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderFollowsTheCluster(t *testing.T) {
	objects, err := Render(testCluster("ipv6", "ipv4"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := data(t, find(t, objects, "ConfigMap", "kube-proxy"), "config.conf")
	if proxy["clusterCIDR"] != "fd00:10:244::/56,10.244.0.0/16" || proxy["bindAddress"] != "::" {
		t.Errorf("kube-proxy = %v", proxy)
	}
	net := data(t, find(t, objects, "ConfigMap", "kube-flannel-cfg"), "net-conf.json")
	if net["Network"] != "10.244.0.0/16" || net["IPv6Network"] != "fd00:10:244::/56" || net["Backend"].(map[string]any)["MTU"] != 1430.0 {
		t.Errorf("flannel = %v", net)
	}
	dns := find(t, objects, "Service", "kube-dns")["spec"].(map[string]any)
	if dns["clusterIP"] != "fd00:10:96::a" || dns["ipFamilies"].([]any)[0] != "IPv6" {
		t.Errorf("kube-dns = %v", dns)
	}
	corefile := find(t, objects, "ConfigMap", "coredns")["data"].(map[string]any)["Corefile"].(string)
	if !strings.Contains(corefile, "kubernetes lab.local in-addr.arpa ip6.arpa {") {
		t.Errorf("Corefile = %s", corefile)
	}
}

func TestRenderWithoutFlannel(t *testing.T) {
	c := testCluster("ipv4")
	c.Flannel = nil
	objects, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o["metadata"].(map[string]any)["namespace"] == "kube-flannel" || o["kind"] == "Namespace" {
			t.Errorf("rendered %v without flannel", o["metadata"])
		}
	}
}
