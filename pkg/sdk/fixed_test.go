package sdk_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/sdk"
)

// The fixture kinds of the group fixed.example.org: Net, of which a node holds several, whose
// spec.outer.domain, spec.ranges, spec.mode (default safe), the map spec.labels, the nullable
// spec.alias, the nullable spec.tier (default gold), spec.group.zone (default z) inside the
// nullable spec.group, spec.pair.key, which both branches of spec.pair's anyOf mark, and the
// integer spec.port, whose default the schema spells 1.0, every node of a cluster holds alike and
// whose notes are free; Site, of which a node holds one, whose spec.host is held alike; and Free,
// which marks nothing.
const (
	netSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "x-chalkos-kind": {"group": "fixed.example.org", "version": "v1", "kind": "Net", "multiple": true},
  "properties": {
    "apiVersion": {"const": "fixed.example.org/v1"},
    "kind": {"const": "Net"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
    "spec": {
      "type": "object",
      "properties": {
        "outer": {
          "type": "object",
          "properties": {"domain": {"type": "string", "x-chalkos-fixed": "cluster"}, "note": {"type": "string"}}
        },
        "ranges": {"type": "array", "items": {"type": "string"}, "x-chalkos-fixed": "cluster"},
        "mode": {"enum": ["fast", "safe"], "default": "safe", "x-chalkos-fixed": "cluster"},
        "labels": {"type": "object", "additionalProperties": {"type": "string"}, "x-chalkos-fixed": "cluster"},
        "alias": {"anyOf": [{"type": "string"}, {"type": "null"}], "default": null, "x-chalkos-fixed": "cluster"},
        "group": {
          "anyOf": [
            {"type": "object", "properties": {"zone": {"type": "string", "default": "z", "x-chalkos-fixed": "cluster"}, "note": {"type": "string"}}},
            {"type": "null"}
          ],
          "default": null
        },
        "tier": {"anyOf": [{"type": "string"}, {"type": "null"}], "default": "gold", "x-chalkos-fixed": "cluster"},
        "pair": {
          "anyOf": [
            {"type": "object", "properties": {"key": {"type": "string", "x-chalkos-fixed": "cluster"}}, "required": ["key"]},
            {"type": "object", "properties": {"key": {"type": "string", "x-chalkos-fixed": "cluster"}, "note": {"type": "string"}}, "required": ["note"]}
          ]
        },
        "port": {"type": "integer", "default": 1.0, "x-chalkos-fixed": "cluster"},
        "note": {"type": "string"}
      }
    }
  }
}`
	siteSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "x-chalkos-kind": {"group": "fixed.example.org", "version": "v1", "kind": "Site", "multiple": false},
  "properties": {
    "apiVersion": {"const": "fixed.example.org/v1"},
    "kind": {"const": "Site"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
    "spec": {"type": "object", "properties": {"host": {"type": "string", "x-chalkos-fixed": "cluster"}}}
  }
}`
	freeSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "x-chalkos-kind": {"group": "fixed.example.org", "version": "v1", "kind": "Free", "multiple": true},
  "properties": {
    "apiVersion": {"const": "fixed.example.org/v1"},
    "kind": {"const": "Free"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
    "spec": {"type": "object", "properties": {"mode": {"type": "string"}}}
  }
}`
)

// fixedContract is the contract of the fixture kinds.
func fixedContract(t *testing.T) sdk.Contract {
	t.Helper()
	return contractOf(t, map[string]string{
		"fixed.example.org_v1_Net.json":  netSchema,
		"fixed.example.org_v1_Site.json": siteSchema,
		"fixed.example.org_v1_Free.json": freeSchema,
	})
}

// contractOf is the contract of the schema files given by name.
func contractOf(t *testing.T, files map[string]string) sdk.Contract {
	t.Helper()
	data := map[string][]byte{}
	for name, s := range files {
		data[name] = []byte(s)
	}
	c, err := sdk.SchemaContract(nil, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fixedDocs parses documents of the fixture kinds, each given as kind, name and spec.
func fixedDocs(t *testing.T, docs ...[3]string) []config.Document {
	t.Helper()
	var yaml []string
	for _, d := range docs {
		yaml = append(yaml, "apiVersion: fixed.example.org/v1\nkind: "+d[0]+"\nmetadata: {name: "+d[1]+"}\nspec: "+d[2]+"\n")
	}
	parsed, err := config.Parse(strings.NewReader(strings.Join(yaml, "---\n")))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// FixedChanges finds the changes of marked fields alone: nested, a list as a whole and with the
// image's defaults; a kind a node holds once pairs whatever the names; unmarked kinds, free
// fields and documents that only one side holds are no change.
func TestFixedChanges(t *testing.T) {
	c := fixedContract(t)
	net := func(spec string) [3]string { return [3]string{"Net", "a", spec} }
	site := [3]string{"Site", "one", "{host: a.example}"}
	free := [3]string{"Free", "a", "{mode: fast}"}
	old := fixedDocs(t, net("{outer: {domain: lab.local, note: x}, ranges: [10.0.0.0/8], note: x}"), site, free)
	for name, tc := range map[string]struct {
		next []config.Document
		want []sdk.FixedChange
	}{
		"unchanged":   {old, nil},
		"free fields": {fixedDocs(t, net("{outer: {domain: lab.local, note: y}, ranges: [10.0.0.0/8], note: y}"), site, [3]string{"Free", "a", "{mode: safe}"}), nil},
		"nested": {fixedDocs(t, net("{outer: {domain: example.org, note: x}, ranges: [10.0.0.0/8], note: x}"), site, free),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.outer.domain", Old: "lab.local", New: "example.org"}}},
		"a list as a whole": {fixedDocs(t, net("{outer: {domain: lab.local, note: x}, ranges: [10.0.0.0/8, 11.0.0.0/8], note: x}"), site, free),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.ranges", Old: []any{"10.0.0.0/8"}, New: []any{"10.0.0.0/8", "11.0.0.0/8"}}}},
		"the default": {fixedDocs(t, net("{outer: {domain: lab.local, note: x}, ranges: [10.0.0.0/8], note: x, mode: safe}"), site, free), nil},
		"off the default": {fixedDocs(t, net("{outer: {domain: lab.local, note: x}, ranges: [10.0.0.0/8], note: x, mode: fast}"), site, free),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.mode", Old: "safe", New: "fast"}}},
		"a single kind whatever its name": {fixedDocs(t, net("{outer: {domain: lab.local, note: x}, ranges: [10.0.0.0/8], note: x}"), [3]string{"Site", "two", "{host: b.example}"}, free),
			[]sdk.FixedChange{{Kind: "Site", Name: "two", Path: "spec.host", Old: "a.example", New: "b.example"}}},
		"documents that come and go": {fixedDocs(t, [3]string{"Net", "b", "{outer: {domain: other.example}}"}, site, free), nil},
	} {
		got, err := sdk.FixedChanges(c, old, tc.next)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FixedChanges =\n%#v\nwant\n%#v", name, got, tc.want)
		}
	}
}

// A map compares as a whole, and the walk finds the marks of a nullable field, on its anyOf, and
// of a field inside a nullable group, below the anyOf, as chalkosLib.mkOption puts them.
func TestFixedChangesOfMapsAndNullables(t *testing.T) {
	c := fixedContract(t)
	net := func(spec string) []config.Document { return fixedDocs(t, [3]string{"Net", "a", spec}) }
	old := net("{labels: {a: x}, alias: one, group: {zone: z1, note: x}}")
	for name, tc := range map[string]struct {
		next []config.Document
		want []sdk.FixedChange
	}{
		"unchanged":           {old, nil},
		"a free field inside": {net("{labels: {a: x}, alias: one, group: {zone: z1, note: y}}"), nil},
		"a map as a whole": {net("{labels: {a: x, b: y}, alias: one, group: {zone: z1, note: x}}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.labels", Old: map[string]any{"a": "x"}, New: map[string]any{"a": "x", "b": "y"}}}},
		"a nullable field": {net("{labels: {a: x}, group: {zone: z1, note: x}}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.alias", Old: "one", New: nil}}},
		"inside a nullable group": {net("{labels: {a: x}, alias: one, group: {zone: z2, note: x}}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.group.zone", Old: "z1", New: "z2"}}},
		"a nullable group that goes": {net("{labels: {a: x}, alias: one}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.group.zone", Old: "z1", New: nil}}},
	} {
		got, err := sdk.FixedChanges(c, old, tc.next)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FixedChanges =\n%#v\nwant\n%#v", name, got, tc.want)
		}
	}
}

// Defaults fill a field left out, also inside a group a document holds empty, but not one written
// as null; a nullable group left out and one written as null are alike.
func TestFixedChangesOfDefaults(t *testing.T) {
	c := fixedContract(t)
	net := func(spec string) []config.Document { return fixedDocs(t, [3]string{"Net", "a", spec}) }
	for name, tc := range map[string]struct {
		old, next []config.Document
		want      []sdk.FixedChange
	}{
		"an empty group takes its defaults": {net("{}"), net("{group: {}}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.group.zone", Old: nil, New: "z"}}},
		"a default written out": {net("{group: {}}"), net("{group: {zone: z}}"), nil},
		"a null group and none": {net("{group: null}"), net("{}"), nil},
		"a group that comes": {net("{}"), net("{group: {zone: z2}}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.group.zone", Old: nil, New: "z2"}}},
		"null against a default": {net("{}"), net("{tier: null}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.tier", Old: "gold", New: nil}}},
		"null where the default is null":  {net("{}"), net("{alias: null}"), nil},
		"an integral default written out": {net("{}"), net("{port: 1}"), nil},
		"off an integral default": {net("{}"), net("{port: 2}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.port", Old: json.Number("1"), New: json.Number("2")}}},
	} {
		got, err := sdk.FixedChanges(c, tc.old, tc.next)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FixedChanges =\n%#v\nwant\n%#v", name, got, tc.want)
		}
	}
}

// The changes of one document come depth first with the fields of each level in name order, and
// a field that two branches of an anyOf mark changes once.
func TestFixedChangesOrderAndBranches(t *testing.T) {
	c := fixedContract(t)
	net := func(spec string) []config.Document { return fixedDocs(t, [3]string{"Net", "a", spec}) }
	for name, tc := range map[string]struct {
		old, next []config.Document
		want      []sdk.FixedChange
	}{
		"several in one document": {
			net("{alias: one, outer: {domain: lab.local}, ranges: [10.0.0.0/8], mode: safe, group: {zone: z1}}"),
			net("{alias: two, outer: {domain: example.org}, ranges: [11.0.0.0/8], mode: fast, group: {zone: z2}}"),
			[]sdk.FixedChange{
				{Kind: "Net", Name: "a", Path: "spec.alias", Old: "one", New: "two"},
				{Kind: "Net", Name: "a", Path: "spec.group.zone", Old: "z1", New: "z2"},
				{Kind: "Net", Name: "a", Path: "spec.mode", Old: "safe", New: "fast"},
				{Kind: "Net", Name: "a", Path: "spec.outer.domain", Old: "lab.local", New: "example.org"},
				{Kind: "Net", Name: "a", Path: "spec.ranges", Old: []any{"10.0.0.0/8"}, New: []any{"11.0.0.0/8"}},
			},
		},
		"two branches mark one field": {net("{pair: {key: a}}"), net("{pair: {key: b, note: x}}"),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.pair.key", Old: "a", New: "b"}}},
	} {
		got, err := sdk.FixedChanges(c, tc.old, tc.next)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FixedChanges =\n%#v\nwant\n%#v", name, got, tc.want)
		}
	}
}

// A kind the contract does not declare marks nothing, even on both sides.
func TestFixedChangesOfAnUndeclaredKind(t *testing.T) {
	old := fixedDocs(t, [3]string{"Other", "a", "{host: a.example}"})
	next := fixedDocs(t, [3]string{"Other", "a", "{host: b.example}"})
	got, err := sdk.FixedChanges(fixedContract(t), old, next)
	if err != nil || got != nil {
		t.Errorf("FixedChanges = %v, %v; want none", got, err)
	}
}

// A contract without schemas cannot tell which fields are fixed.
func TestFixedChangesWithoutSchemas(t *testing.T) {
	if _, err := sdk.FixedChanges(sdk.Contract{}, nil, nil); err == nil {
		t.Error("FixedChanges of a contract without schemas passed")
	}
	if _, err := sdk.FixedChangesBetween(fixedContract(t), sdk.Contract{}, nil, nil); err == nil {
		t.Error("FixedChangesBetween with a contract without schemas passed")
	}
}

// Between two contracts each side takes its own contract's defaults, so two nodes that both
// leave a field out differ where their images default it apart; the marks of either contract
// count, and a kind that only one contract declares keeps that contract's marks.
func TestFixedChangesBetween(t *testing.T) {
	// other is the fixture with mode defaulting to fast, spec.extra marked, and no Site.
	other := contractOf(t, map[string]string{
		"fixed.example.org_v1_Net.json": strings.NewReplacer(
			`"default": "safe"`, `"default": "fast"`,
			`"tier":`, `"extra": {"type": "string", "x-chalkos-fixed": "cluster"}, "tier":`,
		).Replace(netSchema),
	})
	net := func(spec string) [3]string { return [3]string{"Net", "a", spec} }
	for name, tc := range map[string]struct {
		old, next []config.Document
		want      []sdk.FixedChange
	}{
		"defaults apart": {fixedDocs(t, net("{}")), fixedDocs(t, net("{}")),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.mode", Old: "safe", New: "fast"}}},
		"a default written out": {fixedDocs(t, net("{mode: fast}")), fixedDocs(t, net("{}")), nil},
		"a mark of the other contract": {fixedDocs(t, net("{mode: fast, extra: x}")), fixedDocs(t, net("{extra: y}")),
			[]sdk.FixedChange{{Kind: "Net", Name: "a", Path: "spec.extra", Old: "x", New: "y"}}},
		"a kind one contract declares": {fixedDocs(t, net("{mode: fast}"), [3]string{"Site", "one", "{host: a.example}"}),
			fixedDocs(t, net("{}"), [3]string{"Site", "two", "{host: b.example}"}),
			[]sdk.FixedChange{{Kind: "Site", Name: "two", Path: "spec.host", Old: "a.example", New: "b.example"}}},
	} {
		got, err := sdk.FixedChangesBetween(fixedContract(t), other, tc.old, tc.next)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FixedChangesBetween =\n%#v\nwant\n%#v", name, got, tc.want)
		}
	}
}

// Two contracts that declare a kind with a different count per node pair its documents by
// different rules, so FixedChangesBetween refuses them, naming the kind, in either order and
// whether or not the documents hold the kind.
func TestFixedChangesBetweenRefusesAKindDeclaredApart(t *testing.T) {
	single := contractOf(t, map[string]string{
		"fixed.example.org_v1_Net.json":  strings.Replace(netSchema, `"multiple": true`, `"multiple": false`, 1),
		"fixed.example.org_v1_Site.json": siteSchema,
	})
	want := "the images declare Net (fixed.example.org/v1) differently: one holds several per node, the other one"
	docs := fixedDocs(t, [3]string{"Net", "a", "{mode: fast}"})
	for _, tc := range []struct {
		oldC, nextC sdk.Contract
		docs        []config.Document
	}{
		{fixedContract(t), single, docs},
		{single, fixedContract(t), docs},
		{fixedContract(t), single, fixedDocs(t, [3]string{"Site", "one", "{host: a.example}"})},
	} {
		if _, err := sdk.FixedChangesBetween(tc.oldC, tc.nextC, tc.docs, tc.docs); err == nil || err.Error() != want {
			t.Errorf("FixedChangesBetween = %v, want %q", err, want)
		}
	}
}
