package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/schema"
)

// refSchemas are two kinds of an extension as the kinds module writes them: Lane, whose name
// default Zone's lane reserves and which therefore refuses it as a name; and Zone, whose fields
// reference a Lane alone, in a list of nullable references, in list items, in map values, and a
// key of a Secret.
func refSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	const name = `"type": "string", "pattern": "^(?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?)$"`
	const lane = `"x-chalkos-ref": {"apiVersion": "lane.example.org/v1", "kind": "Lane"}`
	envelope := func(kind, group, spec, nameExtra string) []byte {
		return []byte(`{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"title": "` + kind + `", "type": "object",
			"properties": {
				"apiVersion": {"const": "` + group + `/v1"},
				"kind": {"const": "` + kind + `"},
				"metadata": {"type": "object", "properties": {"name": {"type": "string", "pattern": "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"` + nameExtra + `}}, "required": ["name"], "additionalProperties": false},
				"spec": ` + spec + `
			},
			"required": ["apiVersion", "kind", "metadata", "spec"],
			"additionalProperties": false,
			"x-chalkos-kind": {"group": "` + group + `", "version": "v1", "kind": "` + kind + `", "multiple": true}
		}`)
	}
	zone := `{"type": "object", "additionalProperties": false, "properties": {
		"lane": {` + name + `, "default": "default", "x-chalkos-ref": {"apiVersion": "lane.example.org/v1", "kind": "Lane", "reserved": ["default"]}},
		"lanes": {"type": "array", "default": [], "items": {"anyOf": [{` + name + `, ` + lane + `}, {"type": "null"}]}},
		"rules": {"type": "array", "default": [], "items": {"type": "object", "additionalProperties": false, "properties": {"to": {` + name + `, ` + lane + `}}}},
		"byName": {"type": "object", "default": {}, "additionalProperties": {` + name + `, ` + lane + `}},
		"token": {"default": null, "anyOf": [{"type": "object", "required": ["secretRef"], "additionalProperties": false,
			"properties": {"secretRef": {"type": "object", "required": ["key", "name"], "additionalProperties": false,
				"properties": {"key": {"type": "string", "minLength": 1}, "name": {"type": "string", "minLength": 1}}}},
			"x-chalkos-ref": {"apiVersion": "chalkos.dev/v1alpha1", "kind": "Secret", "key": true}}, {"type": "null"}]}
	}}`
	s, err := schema.LoadFiles(map[string][]byte{
		"lane.example.org_v1_Lane.json": envelope("Lane", "lane.example.org", `{"type": "object"}`, `, "not": {"enum": ["default"]}`),
		"zone.example.org_v1_Zone.json": envelope("Zone", "zone.example.org", zone, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func lane(name string) ID { return ID{APIVersion: "lane.example.org/v1", Kind: "Lane", Name: name} }

// parseOne is the one document of yaml.
func parseOne(t *testing.T, yaml string) Document {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs[0]
}

// Refs finds a reference wherever the schema marks one: a field, the items of a list of nullable
// references, a field of list items, the values of a map, and a key of a Secret. Its paths name
// map keys after a dot where they are plain and in quoted brackets where not, and it lists the
// references in the order of the fields' names.
func TestRefs(t *testing.T) {
	d := parseOne(t, `
apiVersion: zone.example.org/v1
kind: Zone
metadata: {name: z}
spec:
  lane: blue
  lanes: [a, null, b]
  rules: [{to: c}, {}]
  byName: {x: d, "a.b": e}
  token: {secretRef: {name: s, key: k}}
`)
	got := Refs(d, refSchemas(t))
	want := []Ref{
		{Path: `spec.byName["a.b"]`, Target: lane("e")},
		{Path: "spec.byName.x", Target: lane("d")},
		{Path: "spec.lane", Target: lane("blue")},
		{Path: "spec.lanes[0]", Target: lane("a")},
		{Path: "spec.lanes[2]", Target: lane("b")},
		{Path: "spec.rules[0].to", Target: lane("c")},
		{Path: "spec.token", Target: ID{APIVersion: CoreAPI, Kind: "Secret", Name: "s"}, Key: "k"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Refs =\n%v\nwant\n%v", got, want)
	}
	if ref, ok := got[6].Secret(); !ok || ref != (SecretRef{Name: "s", Key: "k"}) {
		t.Errorf("Secret() = %v, %v", ref, ok)
	}
	if _, ok := got[0].Secret(); ok {
		t.Error("a reference to a document is no key reference")
	}
}

// A reserved name stands for something that is no document, so it references nothing; a null
// token references no Secret.
func TestRefsSkipsReservedNamesAndNull(t *testing.T) {
	d := parseOne(t, "apiVersion: zone.example.org/v1\nkind: Zone\nmetadata: {name: z}\nspec: {lane: default, token: null}\n")
	if got := Refs(d, refSchemas(t)); got != nil {
		t.Errorf("Refs = %v, want none", got)
	}
}

func TestRefsWithoutSchemas(t *testing.T) {
	d := parseOne(t, "apiVersion: zone.example.org/v1\nkind: Zone\nmetadata: {name: z}\nspec: {lane: blue}\n")
	if got := Refs(d, nil); got != nil {
		t.Errorf("Refs = %v, want none", got)
	}
}
