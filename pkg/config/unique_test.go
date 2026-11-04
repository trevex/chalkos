package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/schema"
)

// poolSchema is an extension kind as the kinds module writes it, with a uniqueness declaration of
// each shape: an integer with a hint and two nullable numbers across the kind; list items across the
// kind; list items within the list, normalised as addresses, ranges and lower case; a registry
// host across the kind; list items keyed by one field and by a tuple with nullable members, two
// declarations on one list; a list within list items, and a field of list items across the kind;
// a nullable object keyed by its fields; and a kind-level tuple of spec fields on the spec itself.
const poolSchema = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title": "Pool", "type": "object",
	"properties": {
		"apiVersion": {"const": "pool.example.org/v1"},
		"kind": {"const": "Pool"},
		"metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
		"spec": {
			"type": "object", "additionalProperties": false,
			"x-chalkos-unique": [{"across": "kind", "by": [{"field": "from"}, {"field": "to"}], "hint": "give each pair one pool"}],
			"properties": {
				"from": {"type": "string", "default": "a"},
				"to": {"type": "string", "default": "b"},
				"table": {"type": "integer", "x-chalkos-unique": [{"across": "kind", "hint": "give each pool a table of its own"}]},
				"weight": {"anyOf": [{"type": "number"}, {"type": "null"}], "default": null, "x-chalkos-unique": [{"across": "kind"}]},
				"zone": {"anyOf": [{"type": "integer"}, {"type": "null"}], "default": null, "x-chalkos-unique": [{"across": "kind"}]},
				"interfaces": {"type": "array", "default": [], "items": {"type": "string"}, "x-chalkos-unique": [{"across": "kind"}]},
				"servers": {"type": "array", "default": [], "items": {"type": "string"}, "x-chalkos-unique": [{"across": "list", "as": "ip"}]},
				"allow": {"type": "array", "default": [], "items": {"type": "string"}, "x-chalkos-unique": [{"across": "list", "as": "cidr"}]},
				"names": {"type": "array", "default": [], "items": {"type": "string"}, "x-chalkos-unique": [{"across": "list", "as": "lower"}]},
				"host": {"type": "string", "default": "", "x-chalkos-unique": [{"across": "kind", "as": "host"}]},
				"reservations": {"type": "array", "default": [], "items": {"type": "object", "properties": {
					"mac": {"type": "string"}, "address": {"type": "string"}}},
					"x-chalkos-unique": [
						{"across": "list", "by": [{"field": "mac", "as": "lower"}]},
						{"across": "list", "by": [{"field": "address", "as": "ip"}], "hint": "reserve each address once"}
					]},
				"entries": {"type": "array", "default": [], "items": {"type": "object", "properties": {
					"prefix": {"type": "string"},
					"ge": {"anyOf": [{"type": "integer"}, {"type": "null"}], "default": null},
					"le": {"anyOf": [{"type": "integer"}, {"type": "null"}], "default": null}}},
					"x-chalkos-unique": [{"across": "list", "by": [{"field": "prefix", "as": "cidr"}, {"field": "ge"}, {"field": "le"}]}]},
				"pools": {"type": "array", "default": [], "items": {"type": "object", "properties": {
					"protocols": {"type": "array", "default": [], "items": {"type": "string"}, "x-chalkos-unique": [{"across": "list"}]},
					"name": {"type": "string", "x-chalkos-unique": [{"across": "kind"}]}}}},
				"disk": {"default": null, "anyOf": [
					{"type": "object", "properties": {"path": {"anyOf": [{"type": "string"}, {"type": "null"}], "default": null}, "model": {"anyOf": [{"type": "string"}, {"type": "null"}], "default": null}}},
					{"type": "null"}],
					"x-chalkos-unique": [{"across": "kind", "by": [{"field": "path"}, {"field": "model"}]}]}
			}
		}
	},
	"required": ["apiVersion", "kind", "metadata", "spec"],
	"additionalProperties": false,
	"x-chalkos-kind": {"group": "pool.example.org", "version": "v1", "kind": "Pool", "multiple": true}
}`

func poolSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadFiles(map[string][]byte{"pool.example.org_v1_Pool.json": []byte(poolSchema)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// poolOf is a Pool document with the spec given in YAML flow style; table defaults to a number
// of its own per first letter, and from to the name, so that only the fields a test gives repeat.
func poolOf(name, spec string) string {
	if !strings.Contains(spec, "table:") {
		spec = "table: " + strconv.Itoa(int(name[0])) + ", " + spec
	}
	if !strings.Contains(spec, "from:") {
		spec = "from: " + name + ", " + spec
	}
	return "---\napiVersion: pool.example.org/v1\nkind: Pool\nmetadata: {name: " + name + "}\nspec: {" + spec + "}\n"
}

// uniqueErr is checkUnique's error for the documents of yaml, as NewSet hands them on: defaults
// filled in and in the set's order.
func uniqueErr(t *testing.T, yaml string) error {
	t.Helper()
	s := poolSchemas(t)
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		if err := s.Validate(docs[i].Object); err != nil {
			t.Fatal(err)
		}
		if docs[i].Object, err = s.Default(docs[i].Object); err != nil {
			t.Fatal(err)
		}
	}
	SortDocuments(docs)
	return checkUnique(docs, s)
}

func TestCheckUnique(t *testing.T) {
	for _, c := range []struct {
		name, yaml, want string
	}{
		{"a number across the kind, with its hint", poolOf("a", "table: 100") + poolOf("b", "table: 100"),
			"Pool/a, Pool/b: spec.table 100 is not unique; give each pool a table of its own"},
		{"numbers compare as numbers", poolOf("a", "table: 100") + poolOf("b", "table: 1e2"),
			"Pool/a, Pool/b: spec.table 100 is not unique; give each pool a table of its own"},
		{"nulls take no part", poolOf("a", "zone: null") + poolOf("b", "") + poolOf("c", "zone: 1"), ""},
		{"a nullable number", poolOf("a", "zone: 1") + poolOf("b", "zone: 1"),
			"Pool/a, Pool/b: spec.zone 1 is not unique"},
		{"list items within one document", poolOf("a", "interfaces: [eth1, eth2, eth1]"),
			`Pool/a: spec.interfaces[0], spec.interfaces[2]: "eth1" is not unique`},
		{"list items across documents", poolOf("a", "interfaces: [eth2, eth1]") + poolOf("b", "interfaces: [eth1]"),
			`Pool/a: spec.interfaces[1], Pool/b: spec.interfaces[0]: "eth1" is not unique`},
		{"one list's items as addresses, the first as written", poolOf("a", `servers: ["2001:db8::1", "2001:db8:0::1"]`),
			`Pool/a: spec.servers[0], spec.servers[1]: "2001:db8::1" is not unique`},
		{"lists of two documents apart", poolOf("a", "servers: [192.0.2.1]") + poolOf("b", "servers: [192.0.2.1]"), ""},
		{"values no normaliser reads take no part", poolOf("a", "servers: [x, x], allow: [y, y]"), ""},
		{"ranges masked", poolOf("a", "allow: [10.0.0.0/8, 10.1.0.0/8]"),
			`Pool/a: spec.allow[0], spec.allow[1]: "10.0.0.0/8" is not unique`},
		{"lower case", poolOf("a", "names: [Lan, lan]"),
			`Pool/a: spec.names[0], spec.names[1]: "Lan" is not unique`},
		{"registry hosts", poolOf("a", "host: Docker.io") + poolOf("b", "host: index.docker.io"),
			`Pool/a, Pool/b: spec.host "Docker.io" is not unique`},
		{"a host keeps its port", poolOf("a", "host: ghcr.io") + poolOf("b", "host: ghcr.io:443"), ""},
		{"items by a field, with a hint", poolOf("a", "reservations: [{mac: AA:01, address: 192.0.2.1}, {mac: aa:01, address: 192.0.2.01}, {mac: aa:02, address: 192.0.2.1}]"),
			"Pool/a: spec.reservations[0].address, spec.reservations[2].address: \"192.0.2.1\" is not unique; reserve each address once\n" +
				`Pool/a: spec.reservations[0].mac, spec.reservations[1].mac: "AA:01" is not unique`},
		{"a tuple's null members compare equal", poolOf("a", "entries: [{prefix: 10.0.0.0/8}, {prefix: 10.0.0.1/8}, {prefix: 10.0.0.0/8, le: 24}]"),
			`Pool/a: (spec.entries[0].prefix, spec.entries[0].ge, spec.entries[0].le), (spec.entries[1].prefix, spec.entries[1].ge, spec.entries[1].le): ("10.0.0.0/8", null, null) is not unique`},
		{"a list within list items is one list each", poolOf("a", "pools: [{protocols: [tcp]}, {protocols: [tcp, udp, udp]}]"),
			`Pool/a: spec.pools[1].protocols[1], spec.pools[1].protocols[2]: "udp" is not unique`},
		{"fractions apart", poolOf("a", "weight: 0.5") + poolOf("b", "weight: 0.25"), ""},
		{"fractions compare as numbers", poolOf("a", "weight: 0.5") + poolOf("b", "weight: 5e-1"),
			"Pool/a, Pool/b: spec.weight 0.5 is not unique"},
		{"a field of list items across the kind", poolOf("a", "pools: [{name: x}, {name: y}]") + poolOf("b", "pools: [{name: z}, {name: x}]"),
			`Pool/a: spec.pools[0].name, Pool/b: spec.pools[1].name: "x" is not unique`},
		{"an object by its fields", poolOf("a", "disk: {path: /dev/vdb}") + poolOf("b", "disk: {path: /dev/vdb}") + poolOf("c", "disk: {path: /dev/vdb, model: m}") + poolOf("d", "disk: null") + poolOf("e", "disk: null"),
			`Pool/a, Pool/b: (spec.disk.path, spec.disk.model) ("/dev/vdb", null) is not unique`},
		{"a kind's tuple of spec fields", poolOf("a", "from: lan, to: wan") + poolOf("b", "from: lan, to: wan") + poolOf("c", "from: lan, to: dmz"),
			`Pool/a, Pool/b: (spec.from, spec.to) ("lan", "wan") is not unique; give each pair one pool`},
		{"every repeat at once, sorted", poolOf("b", "table: 7, interfaces: [eth1]") + poolOf("a", "table: 7, interfaces: [eth1]"),
			"Pool/a, Pool/b: spec.interfaces[0] \"eth1\" is not unique\n" +
				"Pool/a, Pool/b: spec.table 7 is not unique; give each pool a table of its own"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := uniqueErr(t, c.yaml)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != c.want {
				t.Errorf("err =\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// A declaration of a scope or normaliser chalkd does not know refuses the documents it covers, so
// a value it was meant to check never passes unchecked.
func TestCheckUniqueRefusesAnUnknownDeclaration(t *testing.T) {
	for _, bad := range []string{
		`{"across": "node"}`,
		`{"across": "list", "as": "mac"}`,
		`{"across": "list", "by": [{"field": "a", "as": "upper"}]}`,
		`{"across": "list", "within": "x"}`,
	} {
		s, err := schema.LoadFiles(map[string][]byte{"pool.example.org_v1_Pool.json": []byte(
			strings.Replace(poolSchema, `"x-chalkos-unique": [{"across": "list", "as": "lower"}]`, `"x-chalkos-unique": [`+bad+`]`, 1))})
		if err != nil {
			t.Fatal(err)
		}
		d := parseOne(t, poolOf("a", "names: [a]"))
		if err := checkUnique([]Document{d}, s); err == nil || !strings.HasPrefix(err.Error(), "Pool/a: spec.names: x-chalkos-unique of the image's schema: ") {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

// NewSet runs the uniqueness check after the reference check, on the documents with their
// defaults, so chalkctl validate, apply and the image's self-check all refuse a repeat.
func TestNewSetRefusesARepeatedValue(t *testing.T) {
	dir := t.TempDir()
	entries, err := os.ReadDir(builtinSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(builtinSchemas, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "pool.example.org_v1_Pool.json"), []byte(poolSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Parse(strings.NewReader(minimal + poolOf("b", "table: 3") + poolOf("a", "table: 3")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSet(docs, s)
	if err == nil || err.Error() != "Pool/a, Pool/b: spec.table 3 is not unique; give each pool a table of its own" {
		t.Fatalf("err = %v", err)
	}
}
