package sdk_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/sdk"
)

// The fixture kinds of the group fixed.example.org: Net, of which a node holds several, whose
// spec.outer.domain, spec.ranges and spec.mode (default safe) every node of a cluster holds
// alike and whose notes are free; Site, of which a node holds one, whose spec.host is held alike;
// and Free, which marks nothing.
const (
	netSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "x-chalkos-kind": {"group": "fixed.example.org", "version": "v1", "kind": "Net", "multiple": true},
  "x-chalkos-fixed-within": true,
  "properties": {
    "apiVersion": {"const": "fixed.example.org/v1"},
    "kind": {"const": "Net"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
    "spec": {
      "type": "object",
      "x-chalkos-fixed-within": true,
      "properties": {
        "outer": {
          "type": "object",
          "x-chalkos-fixed-within": true,
          "properties": {"domain": {"type": "string", "x-chalkos-fixed": "cluster"}, "note": {"type": "string"}}
        },
        "ranges": {"type": "array", "items": {"type": "string"}, "x-chalkos-fixed": "cluster"},
        "mode": {"enum": ["fast", "safe"], "default": "safe", "x-chalkos-fixed": "cluster"},
        "note": {"type": "string"}
      }
    }
  }
}`
	siteSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "x-chalkos-kind": {"group": "fixed.example.org", "version": "v1", "kind": "Site", "multiple": false},
  "x-chalkos-fixed-within": true,
  "properties": {
    "apiVersion": {"const": "fixed.example.org/v1"},
    "kind": {"const": "Site"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
    "spec": {"type": "object", "x-chalkos-fixed-within": true, "properties": {"host": {"type": "string", "x-chalkos-fixed": "cluster"}}}
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
	c, err := sdk.SchemaContract(nil, map[string][]byte{
		"fixed.example.org_v1_Net.json":  []byte(netSchema),
		"fixed.example.org_v1_Site.json": []byte(siteSchema),
		"fixed.example.org_v1_Free.json": []byte(freeSchema),
	}, nil)
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

// A contract without schemas cannot tell which fields are fixed.
func TestFixedChangesWithoutSchemas(t *testing.T) {
	if _, err := sdk.FixedChanges(sdk.Contract{}, nil, nil); err == nil {
		t.Error("FixedChanges of a contract without schemas passed")
	}
}
