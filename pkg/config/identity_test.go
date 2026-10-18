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
	noKubernetes := strings.Replace(minimal, "spec: {}", `spec: {endpoint: "https://10.0.0.10:6443"}`, 1)
	noType := strings.Replace(minimal, "spec: {type: worker, imageRef: metal}", "spec: {imageRef: metal}", 1) +
		"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: kubernetes}\nspec: {}\n"
	for name, tc := range map[string]struct {
		yaml   string
		target Target
		want   string
	}{
		"type without Kubernetes": {minimal, Target{}, `Node "n1" has type worker, but its image runs no Kubernetes`},
		"no Kubernetes document":  {noKubernetes, Target{Image: &testImage}, "no Kubernetes"},
		"no type":                 {noType, Target{Image: &testImage}, `Node "n1" has no type`},
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
		spec, want string
		families   []string
	}{
		"refused label":            {`{labels: {kubernetes.io/role: x}}`, "kubernetes.io/role", nil},
		"bad address":              {`{nodeIPs: [nope]}`, "not an address", nil},
		"two of a family":          {`{nodeIPs: [10.0.0.1, 10.0.0.2]}`, "more than one ipv4", nil},
		"bad subnet":               {`{validSubnets: [nope]}`, "nope", nil},
		"family the cluster lacks": {`{nodeIPs: [10.0.0.1]}`, "an ipv4 address", []string{"ipv6"}},
		"family without address":   {`{nodeIPs: [], validSubnets: ["10.0.0.0/24"]}`, "cannot pick its ipv6", []string{"ipv4", "ipv6"}},
	} {
		t.Run(name, func(t *testing.T) {
			spec := "{}"
			if tc.families != nil {
				spec = "{ipFamilies: [" + strings.Join(tc.families, ", ") + "]}"
			}
			s := kubernetesSet(t, spec, "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\nspec: "+tc.spec+"\n")
			_, err := s.Identity("n1", Target{Image: &testImage})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestKubeletErrorsReadLikeNix(t *testing.T) {
	s := kubernetesSet(t, "{ipFamilies: [ipv6]}", "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\n"+
		"spec: {nodeIPs: [10.0.0.1, 10.0.0.2, 10.0.0.3, nope]}\n")
	_, err := s.Identity("n1", Target{Image: &testImage})
	want := "invalid Kubernetes settings:\n" +
		"- Kubelet: nodeIPs: \"nope\" is not an address\n" +
		"- Kubelet: nodeIPs: more than one ipv4 address; give at most one per family\n" +
		"- Kubelet: nodeIPs: an ipv4 address, but the cluster's ipFamilies are [ ipv6 ]"
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

func TestIdentityWithoutKubeletUsesDefaults(t *testing.T) {
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
		s := kubernetesSet(t, "{}", "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\nspec: {labels: {\""+key+"\": x}}\n")
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
	s, err := parseSet(t, strings.Replace(minimal, "spec: {type: worker, imageRef: metal}", "spec: {imageRef: metal}", 1)+
		"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: swap}\nspec: {size: 4G, format: swap}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity("n1", Target{}); err != nil {
		t.Fatal(err)
	}
}

// TestKubeletValidSubnets carries a Kubelet's own validSubnets into the identity, leaving every
// address to be picked at boot, and an empty list takes the node's static addresses in place of
// the cluster's subnets.
func TestKubeletValidSubnets(t *testing.T) {
	const network = "---\napiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-a}\nspec: {network: {address: [10.0.0.5/24]}}\n"
	kubelet := func(spec string) string {
		return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\nspec: " + spec + "\n"
	}
	for name, tc := range map[string]struct {
		cluster, kubelet string
		nodeIPs, subnets []string
	}{
		"own subnets":          {"{}", kubelet(`{validSubnets: ["192.168.100.0/24"]}`), []string{}, []string{"192.168.100.0/24"}},
		"empty over cluster's": {`{nodeIP: {validSubnets: ["10.0.0.0/8", "!10.0.0.10/32"]}}`, kubelet(`{validSubnets: []}`), []string{"10.0.0.5"}, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := kubernetesSet(t, tc.cluster, network+tc.kubelet).Identity("n1", Target{Image: &testImage})
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
	kubelet := func(spec string) string {
		return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\nspec: " + spec + "\n"
	}
	for name, tc := range map[string]struct {
		cluster, kubelet string
		nodeIPs          []string
		refused          string
	}{
		"static inside":  {"{ipFamilies: [ipv4, ipv6], " + sources + "}", "", []string{"192.168.100.5", "fd00:100::5"}, ""},
		"fixed outside":  {"{ipFamilies: [ipv4, ipv6], " + sources + "}", kubelet(`{nodeIPs: [10.0.0.5]}`), nil, "10.0.0.5 is outside vxlanSourceSubnets"},
		"subnets inside": {"{ipFamilies: [ipv4, ipv6], " + sources + "}", kubelet(`{validSubnets: ["192.168.100.0/24", "fd00:100::/64"]}`), []string{}, ""},
		"subnet outside": {"{ipFamilies: [ipv4, ipv6], " + sources + "}", kubelet(`{validSubnets: ["192.168.0.0/15", "fd00:100::/64"]}`), nil, "picks its address from 192.168.0.0/15"},
		// Exclusions need not be inside.
		"exclusion outside":      {"{ipFamilies: [ipv4, ipv6], " + sources + "}", kubelet(`{validSubnets: ["192.168.100.0/24", "fd00:100::/64", "!10.0.0.0/8"]}`), []string{}, ""},
		"cluster subnet outside": {`{ipFamilies: [ipv4, ipv6], nodeIP: {validSubnets: ["10.0.0.0/8"]}, ` + sources + "}", "", nil, "picks its address from 10.0.0.0/8"},
		"without sources":        {"{}", kubelet(`{nodeIPs: [10.0.0.5]}`), []string{"10.0.0.5"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := kubernetesSet(t, tc.cluster, network+tc.kubelet).Identity("n1", Target{Image: &testImage})
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
	doc := strings.Replace(minimal, "metadata: {name: n1}", "metadata: {name: doc}", 1)
	kube := strings.Replace(doc, "spec: {}", "spec: {endpoint: \"https://10.0.0.10:6443\"}", 1) +
		"---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: kubernetes}\nspec: {}\n" + volume
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
	if got, want := id.Storage.Disks["fast"].Seed, uuidOf("chalkos-seed:lab/n1/fast"); got != want {
		t.Errorf("seed = %s, want %s, the seed of n1", got, want)
	}
	// Node.spec.hostname still names the host; the Kubernetes node keeps the node's name.
	named := strings.Replace(kube, "spec: {type: worker, imageRef: metal}", "spec: {type: worker, imageRef: metal, hostname: host1}", 1)
	s, err = NewSet(mustParse(t, named), allSchemas(t))
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
