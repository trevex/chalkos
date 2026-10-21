package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/schema"
)

// allSchemas combines the built-in kinds' fixture schemas with the networkd fixture.
func allSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	dir := t.TempDir()
	for _, src := range []string{builtinSchemas, "../../test/fixtures/config/networkd/schemas"} {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	s, err := schema.LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func homelabSet(t *testing.T, node string) *Set {
	t.Helper()
	f, err := os.Open("../../test/fixtures/config/homelab/" + node + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	docs, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSet(docs, allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// golden is the identity testdata/homelab-<node>-identity.json holds for the homelab's node, which
// the node's documents must give.
func golden(t *testing.T, node string) identity.Identity {
	t.Helper()
	data, err := os.ReadFile("testdata/homelab-" + node + "-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var id identity.Identity
	if err := json.Unmarshal(data, &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIdentityMatchesTheGoldenIdentity(t *testing.T) {
	for _, node := range []string{"cp1", "w1"} {
		t.Run(node, func(t *testing.T) {
			want := golden(t, node)
			got, err := homelabSet(t, node).Identity(node, Target{Platform: want.Platform, Image: &testImage})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.StaticAddresses(), want.StaticAddresses()) {
				t.Errorf("static addresses = %v, want %v", got.StaticAddresses(), want.StaticAddresses())
			}
			if g, w := macsOf(got), macsOf(want); !reflect.DeepEqual(g, w) {
				t.Errorf("matched MAC addresses = %v, want %v", g, w)
			}
			got.Network, want.Network = nil, nil
			if !reflect.DeepEqual(got, want) {
				t.Errorf("identity =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// macsOf returns the MAC address each network of the identity matches, by network name: the part
// of the network map besides its addresses that Go reads.
func macsOf(id identity.Identity) map[string]any {
	out := map[string]any{}
	networks, _ := id.Network["networks"].(map[string]any)
	for name, n := range networks {
		network, _ := n.(map[string]any)
		match, _ := network["matchConfig"].(map[string]any)
		out[name] = match["MACAddress"]
	}
	return out
}

func TestIdentityCarriesTheMatchedMAC(t *testing.T) {
	s := kubernetesSet(t, "{}", `---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lab}
spec:
  match: {macAddress: "52:54:00:7b:00:11"}
  network: {address: [10.0.0.11/24]}
`)
	id, err := s.Identity("n1", Target{Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"networks": map[string]any{"10-lab": map[string]any{
		"address":     []any{"10.0.0.11/24"},
		"matchConfig": map[string]any{"MACAddress": "52:54:00:7b:00:11"},
	}}}
	if !reflect.DeepEqual(id.Network, want) {
		t.Errorf("network = %#v, want %#v", id.Network, want)
	}
}

func TestIdentityRejects(t *testing.T) {
	noCluster := minimal + nodeDoc("default", "{type: worker}")
	noNode := minimal + clusterDoc("default", "{}")
	for name, tc := range map[string]struct {
		yaml   string
		target Target
		want   string
	}{
		"no Cluster": {noCluster, Target{Image: &testImage}, "no Cluster"},
		"no Node":    {noNode, Target{Image: &testImage}, "no Node"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSet(mustParse(t, tc.yaml), allSchemas(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Identity("n1", tc.target); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestKubeletChecks(t *testing.T) {
	for name, tc := range map[string]struct {
		node, want string
		families   []string
	}{
		"refused label":            {`labels: {kubernetes.io/role: x}`, "kubernetes.io/role", nil},
		"bad address":              {`nodeIP: {addresses: [nope]}`, "not an address", nil},
		"two of a family":          {`nodeIP: {addresses: [10.0.0.1, 10.0.0.2]}`, "more than one ipv4", nil},
		"bad subnet":               {`nodeIP: {validSubnets: [nope]}`, `Node: nodeIP: validSubnets: "nope"`, nil},
		"family the cluster lacks": {`nodeIP: {addresses: [10.0.0.1]}`, "an ipv4 address", []string{"ipv6"}},
		"family without address":   {`nodeIP: {addresses: [], validSubnets: ["10.0.0.0/24"]}`, "Node: no ipv6 address in nodeIP.addresses, and no ipv6 subnet in the nodeIP.validSubnets that apply, so the node cannot pick its ipv6 address", []string{"ipv4", "ipv6"}},
	} {
		t.Run(name, func(t *testing.T) {
			spec := "{}"
			if tc.families != nil {
				spec = "{ipFamilies: [" + strings.Join(tc.families, ", ") + "]}"
			}
			s := kubernetesNodeSet(t, spec, tc.node, "")
			_, err := s.Identity("n1", Target{Image: &testImage})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestNodeErrorsReadLikeNix(t *testing.T) {
	s := kubernetesNodeSet(t, "{ipFamilies: [ipv6]}", "nodeIP: {addresses: [10.0.0.1, 10.0.0.2, 10.0.0.3, nope]}", "")
	_, err := s.Identity("n1", Target{Image: &testImage})
	want := "invalid Kubernetes settings:\n" +
		"- Node: nodeIP.addresses: \"nope\" is not an address\n" +
		"- Node: nodeIP.addresses: more than one ipv4 address; give at most one per family\n" +
		"- Node: nodeIP.addresses: an ipv4 address, but the cluster's ipFamilies are [ ipv6 ]"
	if err == nil || err.Error() != want {
		t.Fatalf("err =\n%v\nwant\n%s", err, want)
	}
}

func TestDefaultNodeIPsTakeTheFirstStaticAddressOfEachFamily(t *testing.T) {
	networks := `---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-a}
spec:
  network: {address: [fd00::1/64, 10.0.0.1/24, 10.0.0.2/24]}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-b}
spec:
  network: {address: [fd00::2/64]}
`
	s := kubernetesSet(t, "{ipFamilies: [ipv6, ipv4]}", networks)
	id, err := s.Identity("n1", Target{Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fd00::1", "10.0.0.1"}; !reflect.DeepEqual(id.Kubernetes.NodeIPs, want) {
		t.Errorf("nodeIPs = %v, want %v", id.Kubernetes.NodeIPs, want)
	}
	// Subnets that apply leave every family to be picked at boot.
	s = kubernetesSet(t, `{nodeIP: {validSubnets: ["10.0.0.0/8"]}}`, networks)
	if id, err = s.Identity("n1", Target{Image: &testImage}); err != nil {
		t.Fatal(err)
	}
	if id.Kubernetes.NodeIPs == nil || len(id.Kubernetes.NodeIPs) != 0 {
		t.Errorf("nodeIPs = %#v, want []", id.Kubernetes.NodeIPs)
	}
}

func TestSwapVolumeOnKubernetesNodeFails(t *testing.T) {
	s := kubernetesSet(t, "{}", "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: swap}\nspec: {size: 4G, format: swap}\n")
	if _, err := s.Identity("n1", Target{Image: &testImage}); err == nil || !strings.Contains(err.Error(), "swap") {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentityWithAnEmptyNodeUsesDefaults(t *testing.T) {
	s := kubernetesSet(t, "{}", "")
	id, err := s.Identity("n1", Target{Platform: "kvm", Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	if id.Hostname != "n1" || id.Kubernetes.NodeName != "n1" || len(id.Time.Servers) != 3 || id.Labels == nil || id.Taints == nil {
		t.Errorf("identity = %+v", id)
	}
}

func mustParse(t *testing.T, yaml string) []Document {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// The identity names the platform its image is built for and no role: the image's schemas, not a
// role, decide which config a node takes.
func TestIdentityNamesNoRole(t *testing.T) {
	id, err := kubernetesSet(t, "{}", "").Identity("n1", Target{Platform: "kvm", Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"role"`) || id.Platform != "kvm" {
		t.Errorf("identity = %s, want the platform kvm and no role", data)
	}
}

// TestKubeletLabels refuses the labels the kubelet will not set on its own node, labels in the
// kubernetes.io and k8s.io namespaces outside its allowlist, and takes every other label.
func TestKubeletLabels(t *testing.T) {
	label := func(key string) error {
		s := kubernetesNodeSet(t, "{}", "labels: {\""+key+"\": x}", "")
		_, err := s.Identity("n1", Target{Image: &testImage})
		return err
	}
	for _, key := range []string{
		"kubernetes.io/role",
		"node-role.kubernetes.io/control-plane",
		"beta.kubernetes.io/fluentd",
		"k8s.io/team",
		"storage.k8s.io/class",
	} {
		if err := label(key); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("label %s: err = %v, want a refusal naming it", key, err)
		}
	}
	for _, key := range []string{
		"kubernetes.io/hostname",
		"kubernetes.io/arch",
		"kubernetes.io/os",
		"beta.kubernetes.io/arch",
		"beta.kubernetes.io/os",
		"beta.kubernetes.io/instance-type",
		"failure-domain.beta.kubernetes.io/region",
		"failure-domain.beta.kubernetes.io/zone",
		"topology.kubernetes.io/region",
		"topology.kubernetes.io/zone",
		"node.kubernetes.io/instance-type",
		"node.kubernetes.io/storage",
		"kubelet.kubernetes.io/pool",
		"a.node.kubernetes.io/b",
		"a.kubelet.kubernetes.io/b",
		"example.com/team",
		"team",
	} {
		if err := label(key); err != nil {
			t.Errorf("label %s: %v", key, err)
		}
	}
}

// TestSwapVolumeWithoutKubernetes takes a swap volume on a node whose image runs no Kubernetes,
// as no kubelet refuses it there.
func TestSwapVolumeWithoutKubernetes(t *testing.T) {
	s, err := parseSet(t, minimal+
		"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: swap}\nspec: {size: 4G, format: swap}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity("n1", Target{}); err != nil {
		t.Fatal(err)
	}
}

// TestNodeValidSubnets carries a Node's own nodeIP.validSubnets into the identity, leaving every
// address to be picked at boot. They replace the Cluster's as a whole: an empty list takes the
// node's static addresses in place of the cluster's subnets.
func TestNodeValidSubnets(t *testing.T) {
	const network = "---\napiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-a}\nspec: {network: {address: [10.0.0.5/24]}}\n"
	for name, tc := range map[string]struct {
		cluster, node    string
		nodeIPs, subnets []string
	}{
		"own subnets":          {"{}", `nodeIP: {validSubnets: ["192.168.100.0/24"]}`, []string{}, []string{"192.168.100.0/24"}},
		"empty over cluster's": {`{nodeIP: {validSubnets: ["10.0.0.0/8", "!10.0.0.10/32"]}}`, `nodeIP: {validSubnets: []}`, []string{"10.0.0.5"}, []string{}},
		"own over cluster's":   {`{nodeIP: {validSubnets: ["10.0.0.0/8"]}}`, `nodeIP: {validSubnets: ["192.168.100.0/24"]}`, []string{}, []string{"192.168.100.0/24"}},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := kubernetesNodeSet(t, tc.cluster, tc.node, network).Identity("n1", Target{Image: &testImage})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(id.Kubernetes.NodeIPs, tc.nodeIPs) || !reflect.DeepEqual(id.Kubernetes.ValidSubnets, tc.subnets) {
				t.Errorf("nodeIPs = %#v, validSubnets = %#v, want %#v and %#v", id.Kubernetes.NodeIPs, id.Kubernetes.ValidSubnets, tc.nodeIPs, tc.subnets)
			}
		})
	}
}

// TestVXLANSourcesHoldTheNodesAddresses refuses a node whose addresses, fixed or picked from the
// subnets that apply, may lie outside the ranges the other nodes take VXLAN from.
func TestVXLANSourcesHoldTheNodesAddresses(t *testing.T) {
	const network = "---\napiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-a}\nspec: {network: {address: [192.168.100.5/24, \"fd00:100::5/64\"]}}\n"
	const sources = `vxlanSourceSubnets: ["192.168.0.0/16", "fd00:100::/64"]`
	for name, tc := range map[string]struct {
		cluster, node string
		nodeIPs       []string
		refused       string
	}{
		"static inside":  {"{ipFamilies: [ipv4, ipv6], " + sources + "}", "", []string{"192.168.100.5", "fd00:100::5"}, ""},
		"fixed outside":  {"{ipFamilies: [ipv4, ipv6], " + sources + "}", `nodeIP: {addresses: [10.0.0.5]}`, nil, "10.0.0.5 is outside vxlanSourceSubnets"},
		"subnets inside": {"{ipFamilies: [ipv4, ipv6], " + sources + "}", `nodeIP: {validSubnets: ["192.168.100.0/24", "fd00:100::/64"]}`, []string{}, ""},
		"subnet outside": {"{ipFamilies: [ipv4, ipv6], " + sources + "}", `nodeIP: {validSubnets: ["192.168.0.0/15", "fd00:100::/64"]}`, nil, "picks its address from 192.168.0.0/15"},
		// Exclusions need not be inside.
		"exclusion outside":      {"{ipFamilies: [ipv4, ipv6], " + sources + "}", `nodeIP: {validSubnets: ["192.168.100.0/24", "fd00:100::/64", "!10.0.0.0/8"]}`, []string{}, ""},
		"cluster subnet outside": {`{ipFamilies: [ipv4, ipv6], nodeIP: {validSubnets: ["10.0.0.0/8"]}, ` + sources + "}", "", nil, "picks its address from 10.0.0.0/8"},
		"without sources":        {"{}", `nodeIP: {addresses: [10.0.0.5]}`, []string{"10.0.0.5"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := kubernetesNodeSet(t, tc.cluster, tc.node, network).Identity("n1", Target{Image: &testImage})
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(id.Kubernetes.NodeIPs, tc.nodeIPs) {
				t.Errorf("nodeIPs = %#v, want %#v", id.Kubernetes.NodeIPs, tc.nodeIPs)
			}
		})
	}
}

// TestIdentityTakesTheNodesName derives the hostname, the Kubernetes node name and the storage
// seed from the name the node is given, the inventory's name its node certificate carries, and
// not from the Node document's name.
func TestIdentityTakesTheNodesName(t *testing.T) {
	const volume = "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: fast}\nspec: {disk: {path: /dev/vdb}}\n"
	kube := minimal + nodeDoc("doc", "{type: worker}") +
		clusterDoc("default", "{}") + volume
	s, err := NewSet(mustParse(t, kube), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity("n1", Target{Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	if id.Hostname != "n1" || id.Kubernetes == nil || id.Kubernetes.NodeName != "n1" {
		t.Errorf("hostname = %q, kubernetes = %+v, want n1 for both", id.Hostname, id.Kubernetes)
	}
	if got, want := id.Storage.Disks["fast"].Seed, uuidOf("chalkos-seed:n1/fast"); got != want {
		t.Errorf("seed = %s, want %s, the seed of n1", got, want)
	}
	// A Host's hostname names the host; the Kubernetes node keeps the node's name.
	s, err = NewSet(mustParse(t, kube+hostDoc("host1")), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if id, err = s.Identity("n1", Target{Image: &testImage}); err != nil {
		t.Fatal(err)
	}
	if id.Hostname != "host1" || id.Kubernetes.NodeName != "n1" {
		t.Errorf("hostname = %q, node name = %q, want host1 and n1", id.Hostname, id.Kubernetes.NodeName)
	}
	if _, err := s.Identity("", Target{Image: &testImage}); err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("err = %v, want a refusal of a node without a name", err)
	}
}

// hostDoc is a Host document of the hostname, given as YAML.
func hostDoc(hostname string) string {
	return "---\napiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {hostname: " + hostname + "}\n"
}

// TestIdentityHostname sets the hostname a Host document gives, and the node's name without one
// or for a Host whose hostname is null.
func TestIdentityHostname(t *testing.T) {
	for name, tc := range map[string]struct{ docs, want string }{
		"a Host":                    {hostDoc("host1"), "host1"},
		"a Host without a hostname": {hostDoc("null"), "n1"},
		"a Host with an empty spec": {"---\napiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {}\n", "n1"},
		"no Host":                   {"", "n1"},
	} {
		t.Run(name, func(t *testing.T) {
			// A node of an image without Kubernetes, which holds no Node.
			s, err := parseSet(t, minimal+tc.docs)
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.Identity("n1", Target{})
			if err != nil {
				t.Fatal(err)
			}
			if id.Hostname != tc.want {
				t.Errorf("hostname = %q, want %q", id.Hostname, tc.want)
			}
		})
	}
}
