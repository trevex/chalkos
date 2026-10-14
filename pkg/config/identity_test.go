package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
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
	for node, kind := range map[string]string{"cp1": kubernetes.KindControlPlane, "w1": kubernetes.KindWorker} {
		t.Run(node, func(t *testing.T) {
			want := golden(t, node)
			got, err := homelabSet(t, node).Identity(Target{Role: want.Role, Platform: want.Platform,
				Kubernetes: &kubernetes.Cluster{Kind: kind}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.StaticAddresses(), want.StaticAddresses()) {
				t.Errorf("static addresses = %v, want %v", got.StaticAddresses(), want.StaticAddresses())
			}
			got.Network, want.Network = nil, nil
			// Extensions move to documents of their own kinds.
			want.Extensions = nil
			if !reflect.DeepEqual(got, want) {
				t.Errorf("identity =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

func TestIdentityRejects(t *testing.T) {
	cp1 := func() *Set { return homelabSet(t, "cp1") }
	for name, tc := range map[string]struct {
		set    *Set
		target Target
		want   string
	}{
		"type of another image":   {cp1(), Target{Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker}}, "controlplane"},
		"type without Kubernetes": {cp1(), Target{}, "Kubernetes"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.set.Identity(tc.target); err == nil || !strings.Contains(err.Error(), tc.want) {
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
			yaml := minimal + "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\nspec: " + tc.spec + "\n"
			s, err := NewSet(mustParse(t, yaml), allSchemas(t))
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Identity(Target{Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker, IPFamilies: tc.families}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestKubeletErrorsReadLikeNix(t *testing.T) {
	yaml := minimal + "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: k}\n" +
		"spec: {nodeIPs: [10.0.0.1, 10.0.0.2, 10.0.0.3, nope]}\n"
	s, err := NewSet(mustParse(t, yaml), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Identity(Target{Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker, IPFamilies: []string{"ipv6"}}})
	want := "invalid Kubernetes settings:\n" +
		"- Kubelet: nodeIPs: \"nope\" is not an address\n" +
		"- Kubelet: nodeIPs: more than one ipv4 address; give at most one per family\n" +
		"- Kubelet: nodeIPs: an ipv4 address, but the cluster's ipFamilies are [ ipv6 ]"
	if err == nil || err.Error() != want {
		t.Fatalf("err =\n%v\nwant\n%s", err, want)
	}
}

func TestDefaultNodeIPsTakeTheFirstStaticAddressOfEachFamily(t *testing.T) {
	yaml := minimal + `---
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
	s, err := NewSet(mustParse(t, yaml), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity(Target{Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker, IPFamilies: []string{"ipv6", "ipv4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fd00::1", "10.0.0.1"}; !reflect.DeepEqual(id.Kubernetes.NodeIPs, want) {
		t.Errorf("nodeIPs = %v, want %v", id.Kubernetes.NodeIPs, want)
	}
	// Subnets that apply leave every family to be picked at boot.
	c := kubernetes.Cluster{Kind: kubernetes.KindWorker, NodeIP: kubernetes.NodeIP{ValidSubnets: []string{"10.0.0.0/8"}}}
	if id, err = s.Identity(Target{Kubernetes: &c}); err != nil {
		t.Fatal(err)
	}
	if id.Kubernetes.NodeIPs == nil || len(id.Kubernetes.NodeIPs) != 0 {
		t.Errorf("nodeIPs = %#v, want []", id.Kubernetes.NodeIPs)
	}
}

func TestSwapVolumeOnKubernetesNodeFails(t *testing.T) {
	yaml := minimal + "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: swap}\nspec: {size: 4G, format: swap}\n"
	s, err := NewSet(mustParse(t, yaml), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity(Target{Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker}}); err == nil || !strings.Contains(err.Error(), "swap") {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentityWithoutKubeletUsesDefaults(t *testing.T) {
	s, err := NewSet(mustParse(t, minimal), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity(Target{Role: "worker", Platform: "kvm", Kubernetes: &kubernetes.Cluster{Kind: kubernetes.KindWorker}})
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
