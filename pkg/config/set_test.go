package config

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/schema"
)

const builtinSchemas = "../../test/fixtures/kinds/builtin"

func schemas(t *testing.T) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadSchemas(builtinSchemas)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const minimal = `
apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec: {}
---
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: metal}
spec: {source: ./images/metal}
---
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: n1}
spec: {type: worker, imageRef: metal}
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec: {disk: {path: /dev/vda}}
`

func parseSet(t *testing.T, yaml string) (*Set, error) {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return NewSet(docs, schemas(t))
}

func TestParseSkipsEmptyDocuments(t *testing.T) {
	docs, err := Parse(strings.NewReader("---\n" + minimal + "\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 4 || docs[2].Kind != "Node" || docs[2].Name != "n1" {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestParseRejectsANonObjectDocument(t *testing.T) {
	if _, err := Parse(strings.NewReader("- a\n- b\n")); err == nil || !strings.Contains(err.Error(), "document 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewSetDefaultsDocuments(t *testing.T) {
	s, err := parseSet(t, minimal)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.One("SystemDisk")
	if !ok {
		t.Fatal("no SystemDisk")
	}
	enc := d.Object["spec"].(map[string]any)["encryption"].(map[string]any)
	if enc["mode"] != "tpm2" {
		t.Errorf("encryption = %v", enc)
	}
}

func TestNewSetRejects(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"missing Cluster": {strings.Replace(minimal,
			"apiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: lab}\nspec: {}\n---\n", "", 1), "Cluster"},
		"two Nodes": {minimal + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: n2}\nspec: {imageRef: metal}\n",
			"more than one Node"},
		"unknown kind": {minimal + "---\napiVersion: x.example.org/v1\nkind: Thing\nmetadata: {name: a}\nspec: {}\n",
			`does not accept kind "Thing"`},
		"missing image": {strings.Replace(minimal, "imageRef: metal", "imageRef: kvm", 1), `Image "kvm"`},
		"duplicate name": {minimal + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Image\nmetadata: {name: metal}\nspec: {source: ./x}\n",
			`two Image documents named "metal"`},
		"invalid document": {strings.Replace(minimal, "{path: /dev/vda}", "{path: vda}", 1), "SystemDisk"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSet(t, tc.yaml); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestResolveSecret(t *testing.T) {
	s, err := parseSet(t, minimal+"---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: bgp}\nspec: {data: {password: s3cret}}\n")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ResolveSecret(SecretRef{Name: "bgp", Key: "password"})
	if err != nil || v != "s3cret" {
		t.Fatalf("ResolveSecret = %q, %v", v, err)
	}
	if _, err := s.ResolveSecret(SecretRef{Name: "bgp", Key: "nope"}); err == nil {
		t.Error("resolved a missing key")
	}
}

func TestCheckSecretRefsFindsReferencesAtAnyDepth(t *testing.T) {
	docs := []Document{{APIVersion: "x/v1", Kind: "X", Name: "a", Object: map[string]any{
		"spec": map[string]any{"list": []any{map[string]any{"token": map[string]any{
			"secretRef": map[string]any{"name": "missing", "key": "k"}}}}},
	}}}
	err := checkSecretRefs(docs, map[string]map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `X "a"`) || !strings.Contains(err.Error(), `Secret "missing"`) {
		t.Fatalf("err = %v", err)
	}
}
