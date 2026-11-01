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

// Refs follows $ref into the schema's $defs, as the converter writes a repeated section, also
// from one definition into another, and finds the references the definitions mark.
func TestRefsFollowsDefs(t *testing.T) {
	s, err := schema.LoadFiles(map[string][]byte{"hop.example.org_v1_Hop.json": []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"apiVersion": {"const": "hop.example.org/v1"}, "kind": {"const": "Hop"},
			"metadata": {"type": "object"},
			"spec": {"type": "object", "properties": {
				"first": {"$ref": "#/$defs/Route"},
				"routes": {"type": "array", "items": {"$ref": "#/$defs/Route"}}
			}}
		},
		"$defs": {
			"Route": {"type": "object", "properties": {"via": {"$ref": "#/$defs/LaneName"}}},
			"LaneName": {"type": "string", "x-chalkos-ref": {"apiVersion": "lane.example.org/v1", "kind": "Lane"}}
		},
		"x-chalkos-kind": {"group": "hop.example.org", "version": "v1", "kind": "Hop", "multiple": true}
	}`)})
	if err != nil {
		t.Fatal(err)
	}
	d := parseOne(t, "apiVersion: hop.example.org/v1\nkind: Hop\nmetadata: {name: h}\nspec: {first: {via: a}, routes: [{via: b}, {}]}\n")
	want := []Ref{
		{Path: "spec.first.via", Target: lane("a")},
		{Path: "spec.routes[0].via", Target: lane("b")},
	}
	if got := Refs(d, s); !reflect.DeepEqual(got, want) {
		t.Errorf("Refs =\n%v\nwant\n%v", got, want)
	}
}

// laneOf is a Lane document.
func laneOf(name string) string {
	return "---\napiVersion: lane.example.org/v1\nkind: Lane\nmetadata: {name: " + name + "}\nspec: {}\n"
}

// zoneOf is a Zone document with the spec given in YAML flow style.
func zoneOf(name, spec string) string {
	return "---\napiVersion: zone.example.org/v1\nkind: Zone\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

func parseAll(t *testing.T, yaml string) Documents {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	SortDocuments(docs)
	return docs
}

// checkRefs reports every reference that does not resolve, one line each, naming the document
// that holds it, the field, and the target.
func TestCheckRefsReportsEveryUnresolvedReference(t *testing.T) {
	docs := parseAll(t, zoneOf("z", "{lane: blue, lanes: [a, b], token: {secretRef: {name: s, key: nope}}}")+
		laneOf("a")+
		"---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: s}\nspec: {data: {k: v}}\n")
	_, err := checkRefs(docs, refSchemas(t))
	want := `Zone/z: spec.lane: Lane "blue" does not exist
Zone/z: spec.lanes[1]: Lane "b" does not exist
Zone/z: spec.token: Secret "s" has no key "nope"`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want\n%s", err, want)
	}
}

// A set is applied whole, so removing a document others reference is refused by naming each
// referrer.
func TestCheckRefsNamesEachReferrerOfAMissingDocument(t *testing.T) {
	docs := parseAll(t, zoneOf("x", "{lanes: [gone]}")+zoneOf("y", "{rules: [{to: gone}]}"))
	_, err := checkRefs(docs, refSchemas(t))
	want := `Zone/x: spec.lanes[0]: Lane "gone" does not exist
Zone/y: spec.rules[0].to: Lane "gone" does not exist`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want\n%s", err, want)
	}
}

// checkRefs returns each document's references, by its ID; a document without any has no entry.
func TestCheckRefsReturnsTheRefsByDocument(t *testing.T) {
	docs := parseAll(t, zoneOf("z", "{lane: default, lanes: [a]}")+laneOf("a"))
	refs, err := checkRefs(docs, refSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[ID][]Ref{{APIVersion: "zone.example.org/v1", Kind: "Zone", Name: "z"}: {{Path: "spec.lanes[0]", Target: lane("a")}}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %v, want %v", refs, want)
	}
}
