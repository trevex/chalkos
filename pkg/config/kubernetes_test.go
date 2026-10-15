package config

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// testImage is the facts of a flannel image.
var testImage = kubernetes.Image{Version: "1.37.1", CNI: kubernetes.CNIFlannel, Images: kubernetes.Images{
	Etcd: "e", KubeAPIServer: "a", KubeControllerManager: "c", KubeScheduler: "s", KubeProxy: "p", CoreDNS: "d", Flannel: "f",
}}

// kubernetesSet is minimal for a worker with an endpoint and the Kubernetes spec given, plus more
// documents.
func kubernetesSet(t *testing.T, spec, more string) *Set {
	t.Helper()
	yaml := strings.Replace(minimal, "spec: {}", "spec: {endpoint: \"https://10.0.0.10:6443\"}", 1) +
		"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: kubernetes}\nspec: " + spec + "\n" + more
	s, err := NewSet(mustParse(t, yaml), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClusterFromDefaults(t *testing.T) {
	c, err := kubernetesSet(t, "{}", "").Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != kubernetes.KindWorker || c.Endpoint != "https://10.0.0.10:6443" || c.Version != "1.37.1" || c.Images.KubeProxy != "p" {
		t.Errorf("cluster = %+v", c)
	}
	if c.Domain != "cluster.local" || c.PodCIDRs.IPv4 != "10.244.0.0/16" || c.NodeCIDRMaskSizes.IPv6 != 64 || c.NodeIP.Timeout != 300 || c.VIP.Mode != "l2" {
		t.Errorf("defaults = %+v", c)
	}
	// The DNS address defaults to the 10th address of each service range.
	if c.DNSIPs.IPv4 != "10.96.0.10" || c.DNSIPs.IPv6 != "fd00:10:96::a" {
		t.Errorf("dnsIPs = %+v", c.DNSIPs)
	}
	// A flannel image without a Flannel document takes the interface's MTU.
	if c.Flannel == nil || c.Flannel.MTU != 0 {
		t.Errorf("flannel = %+v", c.Flannel)
	}
}

func TestClusterFromSettings(t *testing.T) {
	s := kubernetesSet(t, `{ipFamilies: [ipv6, ipv4], serviceCIDRs: {ipv4: 10.100.0.0/16}, vip: {addresses: [10.0.0.10]},
  extraArgs: {kubelet: {v: "2"}}}`, `---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Flannel
metadata: {name: flannel}
spec: {mtu: 1430}
`)
	c, err := s.Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	if c.Primary() != "ipv6" || c.DNSIPs.IPv4 != "10.100.0.10" || c.Flannel.MTU != 1430 || c.ExtraArgs["kubelet"]["v"] != "2" || len(c.VIP.Addresses) != 1 {
		t.Errorf("cluster = %+v", c)
	}
	none := testImage
	none.CNI, none.Images.Flannel = kubernetes.CNINone, ""
	if c, err := kubernetesSet(t, "{}", "").Cluster(none); err != nil || c.Flannel != nil {
		t.Errorf("without flannel: %+v, %v", c.Flannel, err)
	}
}

func TestClusterRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml, want string
	}{
		"no Kubernetes": {strings.Replace(minimal, "spec: {}", "spec: {endpoint: \"https://10.0.0.10:6443\"}", 1), "no Kubernetes"},
		"no endpoint": {minimal + "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: k}\nspec: {}\n",
			"has no endpoint"},
		"no type": {strings.Replace(strings.Replace(minimal, "type: worker, ", "", 1), "spec: {}", "spec: {endpoint: \"https://10.0.0.10:6443\"}", 1) +
			"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: k}\nspec: {}\n", "has no type"},
		"overlapping ranges": {strings.Replace(minimal, "spec: {}", "spec: {endpoint: \"https://10.0.0.10:6443\"}", 1) +
			"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: k}\nspec: {podCIDRs: {ipv4: 10.96.0.0/16}}\n",
			`Kubernetes "k": podCIDRs.ipv4 10.96.0.0/16 and serviceCIDRs.ipv4 10.96.0.0/12 overlap`},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSet(mustParse(t, tc.yaml), allSchemas(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Cluster(testImage); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestRegistryMirrors(t *testing.T) {
	mirror := func(name, registry, mirrors string, plain bool) string {
		return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: RegistryMirror\nmetadata: {name: " + name + "}\n" +
			"spec: {registry: \"" + registry + "\", mirrors: " + mirrors + ", allowPlainHTTP: " + map[bool]string{true: "true", false: "false"}[plain] + "}\n"
	}
	s := kubernetesSet(t, "{}", mirror("docker.io", "docker.io", "[https://a.example, https://b.example]", false)+
		mirror("local", "localhost:5000", "[http://10.0.2.100:5000]", true))
	got, err := s.RegistryMirrors()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["docker.io"].Mirrors[1] != "https://b.example" || !got["localhost:5000"].AllowPlainHTTP {
		t.Errorf("mirrors = %+v", got)
	}
	for name, more := range map[string]string{
		"plain HTTP": mirror("local", "localhost:5000", "[http://10.0.2.100:5000]", false),
		"twice":      mirror("a", "docker.io", "[https://a.example]", false) + mirror("b", "docker.io", "[https://b.example]", false),
	} {
		if _, err := kubernetesSet(t, "{}", more).RegistryMirrors(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestManifestObjectsInNameOrder(t *testing.T) {
	manifest := func(name string, objects ...string) string {
		var list []string
		for _, o := range objects {
			list = append(list, "{apiVersion: v1, kind: Namespace, metadata: {name: "+o+"}}")
		}
		return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Manifest\nmetadata: {name: " + name + "}\nspec: {objects: [" + strings.Join(list, ", ") + "]}\n"
	}
	s := kubernetesSet(t, "{}", manifest("b", "third")+manifest("a", "first", "second"))
	objects, err := s.ManifestObjects()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objects {
		names = append(names, o["metadata"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, " ") != "first second third" {
		t.Errorf("objects = %v", names)
	}
}

func TestTimeDHCP(t *testing.T) {
	if dhcp, err := kubernetesSet(t, "{}", "").TimeDHCP(); err != nil || dhcp {
		t.Errorf("without TimeSync: %v, %v", dhcp, err)
	}
	s := kubernetesSet(t, "{}", "---\napiVersion: time.chalkos.dev/v1alpha1\nkind: TimeSync\nmetadata: {name: t}\nspec: {dhcp: true}\n")
	if dhcp, err := s.TimeDHCP(); err != nil || !dhcp {
		t.Errorf("with dhcp: %v, %v", dhcp, err)
	}
}

// The identity's address families come from the Kubernetes document.
func TestIdentityTakesTheDocumentsFamilies(t *testing.T) {
	s := kubernetesSet(t, "{ipFamilies: [ipv6]}", "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\nspec: {nodeIPs: [10.0.0.1]}\n")
	_, err := s.Identity(Target{Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker, Version: "1.37.1"}})
	if err == nil || !strings.Contains(err.Error(), "an ipv4 address, but the cluster's ipFamilies are [ ipv6 ]") {
		t.Fatalf("err = %v", err)
	}
}
