package config

import (
	"os"
	"path/filepath"
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

// imageSchemas loads the fixture image whose Demo kind declares a real secretRef field, the way
// the converter emits one: nullable, so an unset token is the anyOf's "null" branch.
func imageSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/image/schemas")
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
kind: Node
metadata: {name: n1}
spec: {type: worker}
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
	if len(docs) != 3 || docs[1].Kind != "Node" || docs[1].Name != "n1" {
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

func TestOneFindsAnExtensionKindByName(t *testing.T) {
	set := &Set{schemas: extraSchemaDir(t), docs: []Document{{
		APIVersion: "extra.example.org/v1", Kind: "Extra", Name: "a",
		Object: map[string]any{
			"apiVersion": "extra.example.org/v1",
			"kind":       "Extra",
			"metadata":   map[string]any{"name": "a"},
			"spec":       map[string]any{"data": map[string]any{}},
		},
	}}}
	d, ok := set.One("Extra")
	if !ok || d.Name != "a" {
		t.Fatalf(`One("Extra") = %+v, %v`, d, ok)
	}
}

func TestOneIsFalseForAMultipleKind(t *testing.T) {
	s, err := parseSet(t, minimal+"---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: a}\nspec: {data: {}}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.One("Secret"); ok {
		t.Error(`One("Secret") = true, want false: Secret is a multiple kind`)
	}
}

func TestNewSetRejects(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"missing Cluster": {strings.Replace(minimal,
			"apiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: lab}\nspec: {}\n---\n", "", 1), "Cluster"},
		"two Nodes": {minimal + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: n2}\nspec: {}\n",
			"more than one Node"},
		"unknown kind": {minimal + "---\napiVersion: x.example.org/v1\nkind: Thing\nmetadata: {name: a}\nspec: {}\n",
			`does not accept kind "Thing"`},
		"duplicate name": {minimal + strings.Repeat("---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: a}\nspec: {data: {}}\n", 2),
			`two Secret documents named "a"`},
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

func TestCheckSecretRefsFindsARealSecretRef(t *testing.T) {
	docs := []Document{{APIVersion: "demo.example.org/v1alpha1", Kind: "Demo", Name: "a", Object: map[string]any{
		"apiVersion": "demo.example.org/v1alpha1",
		"kind":       "Demo",
		"metadata":   map[string]any{"name": "a"},
		"spec": map[string]any{"name": "a", "token": map[string]any{
			"secretRef": map[string]any{"name": "missing", "key": "k"}}},
	}}}
	err := checkSecretRefs(docs, imageSchemas(t), map[string]map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `Demo "a"`) || !strings.Contains(err.Error(), `Secret "missing"`) {
		t.Fatalf("err = %v", err)
	}
}

// extraSchemaDir writes a tiny, extension-like schema to a temp directory: its "data" field is a
// bare object, with no properties of its own, so nothing beneath it ever carries
// x-chalkos-secret-ref.
func extraSchemaDir(t *testing.T) *schema.Schemas {
	t.Helper()
	dir := t.TempDir()
	doc := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "Extra",
		"type": "object",
		"properties": {
			"apiVersion": {"const": "extra.example.org/v1"},
			"kind": {"const": "Extra"},
			"metadata": {
				"type": "object",
				"properties": {"name": {"type": "string"}},
				"required": ["name"]
			},
			"spec": {
				"type": "object",
				"properties": {"data": {"type": "object"}},
				"required": ["data"],
				"additionalProperties": false
			}
		},
		"required": ["apiVersion", "kind", "metadata", "spec"],
		"additionalProperties": false,
		"x-chalkos-kind": {"group": "extra.example.org", "version": "v1", "kind": "Extra", "multiple": false}
	}`
	if err := os.WriteFile(filepath.Join(dir, "extra.example.org_v1_Extra.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCheckSecretRefsIgnoresAnUnmarkedUserMapEntry(t *testing.T) {
	docs := []Document{{APIVersion: "extra.example.org/v1", Kind: "Extra", Name: "a", Object: map[string]any{
		"apiVersion": "extra.example.org/v1",
		"kind":       "Extra",
		"metadata":   map[string]any{"name": "a"},
		"spec": map[string]any{"data": map[string]any{
			"secretRef": map[string]any{"name": "missing", "key": "k"}}},
	}}}
	if err := checkSecretRefs(docs, extraSchemaDir(t), map[string]map[string]string{}); err != nil {
		t.Fatalf("err = %v, want nil: the schema never marks spec.data as a secret reference", err)
	}
}
