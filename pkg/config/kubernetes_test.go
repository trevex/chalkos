package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// testImage is the facts of a flannel image.
var testImage = kubernetes.Image{Version: "1.37.1", CNI: kubernetes.CNIFlannel, Images: kubernetes.Images{
	Etcd: "e", KubeAPIServer: "a", KubeControllerManager: "c", KubeScheduler: "s", KubeProxy: "p", CoreDNS: "d", Flannel: "f",
}}

// kubernetesSet is minimal for a worker with an endpoint and the Cluster spec given, plus more
// documents.
func kubernetesSet(t *testing.T, spec, more string) *Set {
	t.Helper()
	return kubernetesNodeSet(t, spec, "", more)
}

// kubernetesNodeSet is kubernetesSet with the fields node gives, as YAML, in the worker's Node.
func kubernetesNodeSet(t *testing.T, spec, node, more string) *Set {
	t.Helper()
	if node != "" {
		node = ", " + node
	}
	yaml := minimal + clusterDoc("default", spec) +
		nodeDoc("default", "{type: worker"+node+"}") + more
	s, err := NewSet(mustParse(t, yaml), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// nodeDoc is a Kubernetes Node document called name with the spec given as YAML.
func nodeDoc(name, spec string) string {
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

// flannelDoc is a Flannel document called name with the spec given as YAML.
func flannelDoc(name, spec string) string {
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Flannel\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

// clusterDoc is a Kubernetes Cluster document called name with the endpoint https://10.0.0.10:6443
// the fields the YAML mapping spec gives.
func clusterDoc(name, spec string) string {
	fields := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(spec), "{"), "}"))
	if fields != "" {
		fields = ", " + fields
	}
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: " + name + "}\n" +
		`spec: {endpoint: "https://10.0.0.10:6443"` + fields + "}\n"
}

// The cluster's kind is the Kubernetes Node's type.
func TestClusterTakesTheNodesType(t *testing.T) {
	s, err := NewSet(mustParse(t, minimal+
		clusterDoc("k", "{}")+
		nodeDoc("cp", "{type: controlplane}")), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if c, err := s.Documents().Cluster(testImage); err != nil || c.Kind != kubernetes.KindControlPlane {
		t.Errorf("kind = %q, %v, want %q", c.Kind, err, kubernetes.KindControlPlane)
	}
}

func TestClusterFromDefaults(t *testing.T) {
	c, err := kubernetesSet(t, "{}", "").Documents().Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != kubernetes.KindWorker || c.Endpoint != "https://10.0.0.10:6443" || c.Version != "1.37.1" || c.Images.KubeProxy != "p" {
		t.Errorf("cluster = %+v", c)
	}
	if c.Domain != "cluster.local" || c.PodCIDRs.IPv4 != netip.MustParsePrefix("10.244.0.0/16") || c.NodeCIDRMaskSizes.IPv4 != 24 || c.NodeIP.Timeout != 300 || c.VIP.Mode != "l2" {
		t.Errorf("defaults = %+v", c)
	}
	// The DNS address defaults to the 10th address of each service range.
	dual, err := kubernetesSet(t, "{ipFamilies: [ipv4, ipv6]}", "").Documents().Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	if dual.DNSIPs.IPv4 != netip.MustParseAddr("10.96.0.10") || dual.DNSIPs.IPv6 != netip.MustParseAddr("fd00:10:96::a") || dual.NodeCIDRMaskSizes.IPv6 != 64 {
		t.Errorf("dual stack = %+v", dual)
	}
	// A flannel image without a Flannel document takes the interface's MTU.
	if c.Flannel == nil || c.Flannel.MTU != 0 {
		t.Errorf("flannel = %+v", c.Flannel)
	}
}

func TestClusterFromSettings(t *testing.T) {
	s := kubernetesNodeSet(t, `{ipFamilies: [ipv6, ipv4], serviceCIDRs: {ipv4: 10.100.0.0/16}, vip: {addresses: [10.0.0.10]}}`,
		`kubeletExtraArgs: {v: "2"}`, flannelDoc("default", `{mtu: 1430, vxlanSourceSubnets: ["fd00:1::/64", "10.0.0.0/24"]}`))
	c, err := s.Documents().Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	if c.Primary() != "ipv6" || c.DNSIPs.IPv4 != netip.MustParseAddr("10.100.0.10") || c.Flannel.MTU != 1430 || fmt.Sprint(c.Flannel.VXLANSourceSubnets) != "[fd00:1::/64 10.0.0.0/24]" || c.ExtraArgs["kubelet"]["v"] != "2" || len(c.VIP.Addresses) != 1 {
		t.Errorf("cluster = %+v", c)
	}
	none := testImage
	none.CNI, none.Images.Flannel = kubernetes.CNINone, ""
	if c, err := kubernetesSet(t, "{}", "").Documents().Cluster(none); err != nil || c.Flannel != nil {
		t.Errorf("without flannel: %+v, %v", c.Flannel, err)
	}
}

func TestClusterRejects(t *testing.T) {
	worker := nodeDoc("default", "{type: worker}")
	for name, tc := range map[string]struct {
		yaml, want string
	}{
		"no Cluster": {minimal + worker, "no Cluster: the config of a node whose image runs Kubernetes holds one Cluster document"},
		"no Node": {minimal +
			clusterDoc("k", "{}"),
			"no Node: the config of a node whose image runs Kubernetes holds one Node document"},
		"overlapping ranges": {minimal + worker +
			clusterDoc("k", "{podCIDRs: {ipv4: 10.96.0.0/16}}"),
			`Cluster "k": podCIDRs.ipv4 10.96.0.0/16 and serviceCIDRs.ipv4 10.96.0.0/12 overlap`},
		"protected kubelet flag": {minimal + clusterDoc("k", "{}") +
			nodeDoc("w", `{type: worker, kubeletExtraArgs: {anonymous-auth: "true"}}`),
			`Node "w": kubeletExtraArgs: --anonymous-auth is set by chalkos and cannot be overridden`},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSet(mustParse(t, tc.yaml), allSchemas(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Documents().Cluster(testImage); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestAddonObjectsInNameOrder(t *testing.T) {
	addon := func(name string, objects ...string) string {
		var list []string
		for _, o := range objects {
			list = append(list, "{apiVersion: v1, kind: Namespace, metadata: {name: "+o+"}}")
		}
		return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Addon\nmetadata: {name: " + name + "}\nspec: {objects: [" + strings.Join(list, ", ") + "]}\n"
	}
	s := kubernetesSet(t, "{}", addon("b", "third")+addon("a", "first", "second"))
	objects, err := s.Documents().AddonObjects()
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

// The identity's address families come from the Cluster document.
func TestIdentityTakesTheDocumentsFamilies(t *testing.T) {
	s := kubernetesNodeSet(t, "{ipFamilies: [ipv6]}", "nodeIP: {addresses: [10.0.0.1]}", "")
	_, err := s.Identity("n1", Target{Image: &testImage})
	if err == nil || !strings.Contains(err.Error(), "an ipv4 address, but the cluster's ipFamilies are [ ipv6 ]") {
		t.Fatalf("err = %v", err)
	}
}

// The cluster holds the values of its families alone.
func TestClusterKeepsTheFamiliesInUse(t *testing.T) {
	c, err := kubernetesSet(t, `{podCIDRs: {ipv6: "fd00:1::/56"}, serviceCIDRs: {ipv6: "fd00:2::/112"},
  dnsIPs: {ipv6: "fd00:2::b"}, nodeCIDRMaskSizes: {ipv6: 60}}`, "").Documents().Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	// The values of the other families are neither kept nor checked beyond their formats: an IPv4
	// range, a range with host bits and an IPv4 DNS address pass as an IPv4 cluster's IPv6 values.
	if _, err := kubernetesSet(t, `{podCIDRs: {ipv6: "10.0.0.0/8"}, serviceCIDRs: {ipv6: "fd00:2::1/112"}, dnsIPs: {ipv6: "10.0.0.1"}}`, "").Documents().Cluster(testImage); err != nil {
		t.Errorf("an ipv4 cluster checked its ipv6 values: %v", err)
	}
	want := kubernetes.ByFamily[netip.Prefix]{IPv4: netip.MustParsePrefix("10.244.0.0/16")}
	if c.PodCIDRs != want || c.ServiceCIDRs.IPv6.IsValid() || c.DNSIPs != (kubernetes.ByFamily[netip.Addr]{IPv4: netip.MustParseAddr("10.96.0.10")}) ||
		c.NodeCIDRMaskSizes != (kubernetes.ByFamily[int]{IPv4: 24}) {
		t.Errorf("cluster = %+v", c)
	}
}

// The VIP's interface is the Node's, which a worker, holding no VIP, leaves null.
func TestClusterTakesGivenDNSAndVIPInterface(t *testing.T) {
	s, err := NewSet(mustParse(t, minimal+
		clusterDoc("k", `{dnsIPs: {ipv4: 10.96.0.53}, vip: {addresses: [10.0.0.10]}}`)+
		nodeDoc("cp", "{type: controlplane, vip: {interface: lan0}}")), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Documents().Cluster(testImage)
	if err != nil {
		t.Fatal(err)
	}
	if c.DNSIPs.IPv4 != netip.MustParseAddr("10.96.0.53") || c.VIP.Interface != "lan0" || c.VIP.Mode != "l2" {
		t.Errorf("cluster = %+v", c)
	}
	want := `Node "default": vip.interface: only control planes hold the VIP; leave it null on a worker`
	if _, err := kubernetesNodeSet(t, `{vip: {addresses: [10.0.0.10]}}`, "vip: {interface: lan0}", "").Documents().Cluster(testImage); err == nil || err.Error() != want {
		t.Errorf("a worker's VIP interface: err = %v, want %q", err, want)
	}
}

// Errors name the document that holds the setting.
func TestClusterNamesTheDocument(t *testing.T) {
	endpoint := minimal + nodeDoc("default", "{type: worker}") +
		"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: k}\n" + `spec: {endpoint: "https://:6443"}` + "\n"
	s, err := NewSet(mustParse(t, endpoint), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Documents().Cluster(testImage); err == nil || !strings.HasPrefix(err.Error(), `Cluster "k": endpoint`) {
		t.Errorf("endpoint: err = %v", err)
	}
	s = kubernetesSet(t, "{}", flannelDoc("flannel", "{mtu: 600}"))
	if _, err := s.Documents().Cluster(testImage); err == nil || !strings.HasPrefix(err.Error(), `Flannel "flannel": flannel: the MTU 600`) {
		t.Errorf("mtu: err = %v", err)
	}
	// VXLAN sources are flannel's, checked against the Cluster document's families and ranges.
	s = kubernetesSet(t, "{}", flannelDoc("flannel", "{vxlanSourceSubnets: [10.244.0.0/24]}"))
	if _, err := s.Documents().Cluster(testImage); err == nil || err.Error() != `Flannel "flannel": vxlanSourceSubnets: 10.244.0.0/24 overlaps podCIDRs.ipv4 10.244.0.0/16` {
		t.Errorf("vxlanSourceSubnets: err = %v", err)
	}
}

// Addon objects keep their numbers as written, so applying them sends the same digits.
func TestAddonObjectsKeepNumbers(t *testing.T) {
	s := kubernetesSet(t, "{}", "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Addon\nmetadata: {name: m}\n"+
		"spec: {objects: [{apiVersion: v1, kind: ConfigMap, metadata: {name: c}, data: {}, big: 9007199254740993, f: 1.5}]}\n")
	objects, err := s.Documents().AddonObjects()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(objects[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"big":9007199254740993`) || !strings.Contains(string(data), `"f":1.5`) {
		t.Errorf("object = %s", data)
	}
}

// The cluster's endpoint is the Cluster document's.
func TestClusterTakesItsEndpoint(t *testing.T) {
	s, err := NewSet(mustParse(t, minimal+nodeDoc("default", "{type: worker}")+
		"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: k}\n"+
		`spec: {endpoint: "https://api.lab:6443"}`+"\n"), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if c, err := s.Documents().Cluster(testImage); err != nil || c.Endpoint != "https://api.lab:6443" {
		t.Errorf("endpoint = %q, %v", c.Endpoint, err)
	}
}

// The schema requires the endpoint of a Cluster document.
func TestClusterWithoutEndpointIsInvalid(t *testing.T) {
	docs := mustParse(t, minimal+nodeDoc("default", "{type: worker}")+
		"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: k}\nspec: {}\n")
	if _, err := NewSet(docs, allSchemas(t)); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("err = %v, want one naming the endpoint", err)
	}
}

// VXLAN source ranges are flannel's, so the Cluster has none of its own.
func TestClusterWithVXLANSourcesIsInvalid(t *testing.T) {
	docs := mustParse(t, minimal+nodeDoc("default", "{type: worker}")+clusterDoc("default", "{vxlanSourceSubnets: [10.0.0.0/24]}"))
	if _, err := NewSet(docs, allSchemas(t)); err == nil || !strings.Contains(err.Error(), "vxlanSourceSubnets") {
		t.Fatalf("err = %v, want one naming vxlanSourceSubnets", err)
	}
}

// The schemas refuse an address or a range that does not parse, in any family, before the cluster
// is built; the formats ip and cidr name the field.
func TestKubernetesAddressFormats(t *testing.T) {
	const notCIDR = "is not valid cidr: an address range is an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6"
	const notIP = "is not valid ip: an address is an IPv4 address of four numbers from 0 to 255 without leading zeros, or an IPv6 address without a zone"
	worker := nodeDoc("w", "{type: worker}")
	for name, tc := range map[string]struct {
		yaml, want string
	}{
		"pod range": {minimal + worker + clusterDoc("k", "{podCIDRs: {ipv4: 10.244.0.0}}"),
			`Cluster "k": - at '/spec/podCIDRs/ipv4': '10.244.0.0' ` + notCIDR},
		"service range of an unused family": {minimal + worker + clusterDoc("k", `{serviceCIDRs: {ipv6: "fd00:2::1"}}`),
			`Cluster "k": - at '/spec/serviceCIDRs/ipv6': 'fd00:2::1' ` + notCIDR},
		"DNS address": {minimal + worker + clusterDoc("k", "{dnsIPs: {ipv4: dns}}"),
			`Cluster "k": - at '/spec/dnsIPs/ipv4': 'dns' ` + notIP},
		"VIP": {minimal + worker + clusterDoc("k", "{vip: {addresses: [10.0.0.10/32]}}"),
			`Cluster "k": - at '/spec/vip/addresses/0': '10.0.0.10/32' ` + notIP},
		"VXLAN source": {minimal + worker + clusterDoc("k", "{}") + flannelDoc("f", "{vxlanSourceSubnets: [10.0.0.0]}"),
			`Flannel "f": - at '/spec/vxlanSourceSubnets/0': '10.0.0.0' ` + notCIDR},
		"node address": {minimal + clusterDoc("k", "{}") + nodeDoc("w", "{type: worker, nodeIP: {addresses: [nope]}}"),
			`Node "w": - at '/spec/nodeIP/addresses/0': 'nope' ` + notIP},
		"node address with a zone": {minimal + clusterDoc("k", "{}") + nodeDoc("w", `{type: worker, nodeIP: {addresses: ["fe80::1%eth0"]}}`),
			`Node "w": - at '/spec/nodeIP/addresses/0': 'fe80::1%eth0' ` + notIP},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSet(mustParse(t, tc.yaml), allSchemas(t)); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Documents no schema checked, such as a test's, fail to decode a range that does not parse, so
// the cluster is never built from it.
func TestClusterOfUncheckedDocuments(t *testing.T) {
	docs := Documents(mustParse(t, minimal+nodeDoc("w", "{type: worker}")+clusterDoc("k", "{podCIDRs: {ipv4: 10.244.0.0}}")))
	if _, err := docs.Cluster(testImage); err == nil || err.Error() != `Cluster "k": netip.ParsePrefix("10.244.0.0"): no '/'` {
		t.Fatalf("err = %v", err)
	}
}
