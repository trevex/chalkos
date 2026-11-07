package config

import (
	"encoding/json"
	"errors"
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

func TestIdentityCarriesTheMatchedMAC(t *testing.T) {
	s, err := NewSet(mustParse(t, minimal+`---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lab}
spec:
  match: {macAddress: "52:54:00:7b:00:11"}
  network: {address: [10.0.0.11/24]}
`), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity("n1", "")
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
	s, err := NewSet(mustParse(t, minimal), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity("n1", "kvm")
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

// TestSwapVolumeWithoutKubernetes takes a swap volume on a node whose image runs no Kubernetes,
// as no kubelet refuses it there.
func TestSwapVolumeWithoutKubernetes(t *testing.T) {
	s, err := parseSet(t, minimal+
		"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: swap}\nspec: {size: 4G, format: swap}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity("n1", ""); err != nil {
		t.Fatal(err)
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
			id, err := s.Identity("n1", "")
			if err != nil {
				t.Fatal(err)
			}
			if id.Hostname != tc.want {
				t.Errorf("hostname = %q, want %q", id.Hostname, tc.want)
			}
		})
	}
}

// TestTranslateRunsTheTranslatorsInOrder runs the image's translators in their order on the
// identity every image gives, and stops at the first that fails with its error as it is.
func TestTranslateRunsTheTranslatorsInOrder(t *testing.T) {
	var ran []string
	translator := func(name string, err error) Translator {
		return func(s *Set, node string, id *identity.Identity) error {
			if id.Hostname != "n1" || id.Platform != "kvm" || node != "n1" {
				t.Errorf("translator %s: node %q, identity %+v, want n1 on kvm with the common identity", name, node, id)
			}
			ran = append(ran, name)
			if err != nil {
				return err
			}
			return id.SetLayer(name, name)
		}
	}
	docs := mustParse(t, minimal)

	target := Target{Platform: "kvm", Translators: []Translator{translator("a", nil), translator("b", nil)}}
	set, id, err := Translate("n1", docs, allSchemas(t), target)
	if err != nil || set == nil {
		t.Fatalf("Translate = %v, %v", set, err)
	}
	if strings.Join(ran, ",") != "a,b" || string(id.Layers["a"]) != `"a"` || string(id.Layers["b"]) != `"b"` {
		t.Errorf("ran %v, layers %s, want a then b and the part of each", ran, id.Layers)
	}

	ran = nil
	refused := errors.New("refused")
	target.Translators = []Translator{translator("a", nil), translator("b", refused), translator("c", nil)}
	set, id, err = Translate("n1", docs, allSchemas(t), target)
	if err != refused {
		t.Errorf("err = %v, want the translator's error unwrapped", err)
	}
	if set != nil || !reflect.DeepEqual(id, identity.Identity{}) {
		t.Errorf("set = %v, identity = %+v, want neither on an error", set, id)
	}
	if strings.Join(ran, ",") != "a,b" {
		t.Errorf("ran %v, want a and b, stopping at b", ran)
	}
}

// TestIdentityOfAnImageWithoutTranslators gives a node whose image's layers translate nothing
// an empty layers section.
func TestIdentityOfAnImageWithoutTranslators(t *testing.T) {
	_, id, err := Translate("n1", mustParse(t, minimal), allSchemas(t), Target{Platform: "kvm"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"layers":{}`) {
		t.Errorf("identity = %s, want empty layers", data)
	}
}
