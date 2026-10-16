package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/manifest"
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

func golden(t *testing.T, node string) manifest.Identity {
	t.Helper()
	f, err := os.Open("../../test/fixtures/homelab-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := manifest.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return m.Nodes[node].Identity
}

func TestIdentityMatchesTheClusterDefinition(t *testing.T) {
	for _, node := range []string{"cp1", "w1"} {
		t.Run(node, func(t *testing.T) {
			want := golden(t, node)
			got, err := homelabSet(t, node).Identity(Target{Role: want.Role, Platform: want.Platform, Image: &testImage})
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
func macsOf(id manifest.Identity) map[string]any {
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
	id, err := s.Identity(Target{Image: &testImage})
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
			if _, err := s.Identity(tc.target); err == nil || !strings.Contains(err.Error(), tc.want) {
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
			_, err := s.Identity(Target{Image: &testImage})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestKubeletErrorsReadLikeNix(t *testing.T) {
	s := kubernetesSet(t, "{ipFamilies: [ipv6]}", "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\n"+
		"spec: {nodeIPs: [10.0.0.1, 10.0.0.2, 10.0.0.3, nope]}\n")
	_, err := s.Identity(Target{Image: &testImage})
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
	id, err := s.Identity(Target{Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fd00::1", "10.0.0.1"}; !reflect.DeepEqual(id.Kubernetes.NodeIPs, want) {
		t.Errorf("nodeIPs = %v, want %v", id.Kubernetes.NodeIPs, want)
	}
	// Subnets that apply leave every family to be picked at boot.
	s = kubernetesSet(t, `{nodeIP: {validSubnets: ["10.0.0.0/8"]}}`, networks)
	if id, err = s.Identity(Target{Image: &testImage}); err != nil {
		t.Fatal(err)
	}
	if id.Kubernetes.NodeIPs == nil || len(id.Kubernetes.NodeIPs) != 0 {
		t.Errorf("nodeIPs = %#v, want []", id.Kubernetes.NodeIPs)
	}
}

func TestSwapVolumeOnKubernetesNodeFails(t *testing.T) {
	s := kubernetesSet(t, "{}", "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: swap}\nspec: {size: 4G, format: swap}\n")
	if _, err := s.Identity(Target{Image: &testImage}); err == nil || !strings.Contains(err.Error(), "swap") {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentityWithoutKubeletUsesDefaults(t *testing.T) {
	s := kubernetesSet(t, "{}", "")
	id, err := s.Identity(Target{Role: "worker", Platform: "kvm", Image: &testImage})
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
