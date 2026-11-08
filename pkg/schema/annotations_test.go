package schema

import (
	"reflect"
	"strings"
	"testing"
)

// poolSchema is a kind's schema with every annotation chalkos reads, where spec.names holds
// replace's x-chalkos-* keyword.
func poolSchema(replace string) []byte {
	return []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "x-chalkos-kind": {"group": "pool.example.org", "version": "v1", "kind": "Pool", "multiple": true, "withheld": true,
    "rules": [{"rule": "true", "message": "m"}, {"rule": "true", "message": "m", "takes": "reset"}]},
  "properties": {
    "spec": {
      "type": "object",
      "x-chalkos-unique": [{"across": "kind", "by": [{"field": "table"}, {"field": "name", "as": "lower"}], "hint": "h"}],
      "properties": {
        "names": {"type": "array", "items": {"type": "string"}, ` + replace + `},
        "vrf": {"type": "string", "x-chalkos-ref": {"apiVersion": "router.chalkos.dev/v1alpha1", "kind": "VRF", "reserved": ["default"]}},
        "object": {"type": "object", "x-chalkos-kubernetes-object": true},
        "table": {"type": "integer", "x-chalkos-rules": [{"rule": "self == oldSelf", "description": "d"}]},
        "name": {"type": "string"},
        "match": {"$ref": "#/$defs/Match"}
      }
    }
  },
  "$defs": {"Match": {"type": "object", "x-chalkos-networkd-section": "Match", "properties": {"name": {"type": "string", "x-chalkos-networkd-key": "Name"}}}}
}`)
}

func loadPool(t *testing.T, replace string) (*Schemas, error) {
	t.Helper()
	return LoadFiles(map[string][]byte{"pool.example.org_v1_Pool.json": poolSchema(replace)})
}

// Each annotation is decoded once, when the schema compiles, onto the node that holds it.
func TestAnnotationsAreDecodedOnTheirNodes(t *testing.T) {
	s, err := loadPool(t, `"x-chalkos-unique": [{"across": "list", "as": "ip"}]`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Annotations{}
	s.WalkSchema("pool.example.org/v1", "Pool", func(n Node) bool {
		if a := n.Annotations(); !reflect.DeepEqual(a, Annotations{}) {
			got[n.Path] = a
		}
		return true
	})
	want := map[string]Annotations{
		"":                {KindRules: []Rule{{Rule: "true", Message: "m", Takes: Refuse}, {Rule: "true", Message: "m", Takes: Reset}}},
		"spec":            {Unique: []Unique{{Across: "kind", By: []KeyField{{Field: "table"}, {Field: "name", As: "lower"}}, Hint: "h"}}},
		"spec.names":      {Unique: []Unique{{Across: "list", As: "ip"}}},
		"spec.vrf":        {Ref: &Ref{APIVersion: "router.chalkos.dev/v1alpha1", Kind: "VRF", Reserved: []string{"default"}}},
		"spec.object":     {KubernetesObject: true},
		"spec.table":      {Rules: []Rule{{Rule: "self == oldSelf", Description: "d", Takes: Refuse}}},
		"spec.match":      {NetworkdSection: "Match"},
		"spec.match.name": {NetworkdKey: "Name"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("annotations\n%#v\nwant\n%#v", got, want)
	}
	if k := s.Kinds()[0]; !k.Multiple || !k.Withheld {
		t.Errorf("kind = %+v", k)
	}
}

// A malformed annotation, or an x-chalkos-* keyword chalkos does not read, fails the schema's load
// and names where it is.
func TestLoadRefusesAMalformedAnnotation(t *testing.T) {
	for _, c := range []struct{ keyword, want string }{
		{`"x-chalkos-unique": [{"across": "node"}]`, `across "node" is neither kind nor list`},
		{`"x-chalkos-unique": [{"across": "list", "as": "mac"}]`, `the normaliser "mac" is none of cidr, host, ip, lower`},
		{`"x-chalkos-unique": [{"across": "list", "by": [{"field": "a", "as": "upper"}]}]`, `the normaliser "upper"`},
		{`"x-chalkos-unique": [{"across": "list", "within": "x"}]`, `unknown field "within"`},
		{`"x-chalkos-rules": [{"rule": "true", "takes": "later"}]`, `takes "later" is none of refuse, reset and reinstall`},
		{`"x-chalkos-rules": [{"rule": "true", "mesage": "m"}]`, `unknown field "mesage"`},
		{`"x-chalkos-ref": {"kind": "VRF"}`, `names no apiVersion or no kind`},
		{`"x-chalkos-kubernetes-object": false`, `is not true`},
		{`"x-chalkos-networkd-key": ""`, `is not a non-empty string`},
		{`"x-chalkos-kind": {"group": "a", "version": "v1", "kind": "A"}`, `only a schema's root declares its kind`},
		{`"x-chalkos-max": 3`, `x-chalkos-max: is no keyword of chalkos's`},
	} {
		_, err := loadPool(t, c.keyword)
		if err == nil || !strings.Contains(err.Error(), "at '/properties/spec/properties/names': ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one at spec.names saying %q", c.keyword, err, c.want)
		}
	}
}

// An annotation beside $ref would be read by no walk, which reads the definition in its place.
func TestLoadRefusesAnAnnotationBesideRef(t *testing.T) {
	_, err := LoadFiles(map[string][]byte{"a.example.org_v1_A.json": []byte(`{
  "x-chalkos-kind": {"group": "a.example.org", "version": "v1", "kind": "A"},
  "properties": {"x": {"$ref": "#/$defs/X", "x-chalkos-rules": [{"rule": "true"}]}},
  "$defs": {"X": {"type": "string"}}}`)})
	if err == nil || !strings.Contains(err.Error(), "at '/properties/x': x-chalkos-rules beside $ref") {
		t.Errorf("err = %v", err)
	}
}

// x-chalkos-kind is decoded strictly, its rules with it.
func TestLoadRefusesAMalformedKind(t *testing.T) {
	for _, c := range []struct{ kind, want string }{
		{`{"group": "a.example.org", "version": "v1", "kind": "A", "many": true}`, `x-chalkos-kind: json: unknown field "many"`},
		{`{"group": "a.example.org", "version": "v1", "kind": "A", "rules": [{"rule": "true", "takes": "x"}]}`, `x-chalkos-kind: rules: takes "x"`},
	} {
		_, err := LoadFiles(map[string][]byte{"a.example.org_v1_A.json": []byte(`{"x-chalkos-kind": ` + c.kind + `}`)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.kind, err, c.want)
		}
	}
}
