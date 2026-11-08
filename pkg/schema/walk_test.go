package schema

import (
	"reflect"
	"testing"
)

// walkSchema is a kind's schema with a property of each shape the walks follow.
const walkSchemaJSON = `{
  "x-chalkos-kind": {"group": "w.example.org", "version": "v1", "kind": "W"},
  "type": "object",
  "properties": {
    "spec": {"type": "object", "properties": {
      "skip": {"type": "object", "properties": {"inner": {"type": "string"}}},
      "rules": {"type": "array", "items": {"type": "object", "properties": {
        "to": {"anyOf": [{"type": "string"}, {"type": "null"}]}
      }}},
      "byName": {"type": "object", "additionalProperties": {"type": "integer"}},
      "section": {"anyOf": [{"$ref": "#/$defs/S"}, {"type": "array", "items": {"$ref": "#/$defs/S"}}]},
      "tree": {"$ref": "#/$defs/T"}
    }}
  },
  "$defs": {
    "S": {"type": "object", "properties": {"key": {"type": "string"}}},
    "T": {"type": "object", "properties": {"child": {"$ref": "#/$defs/T"}}}
  }
}`

func loadWalk(t *testing.T) *Schemas {
	t.Helper()
	s, err := LoadFiles(map[string][]byte{"w.example.org_v1_W.json": []byte(walkSchemaJSON)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Walk reaches every node the value has, an anyOf's node before the branch the value takes with
// the same paths, and names list items with [] and map values with <name> in the Field, which is
// the same in every document; it skips below a node whose visit returns false, and leaves a key
// the schema does not describe alone.
func TestWalk(t *testing.T) {
	s := loadWalk(t)
	v := map[string]any{"spec": map[string]any{
		"skip":    map[string]any{"inner": "x"},
		"rules":   []any{map[string]any{"to": "a"}, map[string]any{"to": nil}},
		"byName":  map[string]any{"a.b": 1},
		"section": []any{map[string]any{"key": "k"}},
		"unknown": "y",
	}}
	type visit struct {
		path, field string
		types       string
	}
	var got []visit
	s.Walk("w.example.org/v1", "W", v, func(n Node) bool {
		types := ""
		if n.Schema.Types != nil {
			types = n.Schema.Types.String()
		}
		got = append(got, visit{n.Path, FieldPath(n.Field), types})
		return n.Path != "spec.skip"
	})
	want := []visit{
		{"", "", "[object]"},
		{"spec", "spec", "[object]"},
		{"spec.byName", "spec.byName", "[object]"},
		{`spec.byName["a.b"]`, "spec.byName.<name>", "[integer]"},
		{"spec.rules", "spec.rules", "[array]"},
		{"spec.rules[0]", "spec.rules[]", "[object]"},
		{"spec.rules[0].to", "spec.rules[].to", ""},
		{"spec.rules[0].to", "spec.rules[].to", "[string]"},
		{"spec.rules[1]", "spec.rules[]", "[object]"},
		{"spec.rules[1].to", "spec.rules[].to", ""},
		{"spec.rules[1].to", "spec.rules[].to", "[null]"},
		{"spec.section", "spec.section", ""},
		{"spec.section", "spec.section", "[array]"},
		{"spec.section[0]", "spec.section[]", "[object]"},
		{"spec.section[0].key", "spec.section[].key", "[string]"},
		{"spec.skip", "spec.skip", "[object]"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Walk visited\n%v\nwant\n%v", got, want)
	}
}

// WalkSchema reaches every node of the schema: properties by name, a map's values, a list's items
// and every branch of an anyOf, $ref followed, but not a schema again below itself.
func TestWalkSchema(t *testing.T) {
	s := loadWalk(t)
	var got []string
	s.WalkSchema("w.example.org/v1", "W", func(n Node) bool {
		got = append(got, n.Path)
		return n.Path != "spec.skip"
	})
	want := []string{
		"",
		"spec",
		"spec.byName", "spec.byName.<name>",
		"spec.rules", "spec.rules[]", "spec.rules[].to", "spec.rules[].to", "spec.rules[].to",
		"spec.section", "spec.section", "spec.section.key", "spec.section", "spec.section[]", "spec.section[].key",
		"spec.skip",
		"spec.tree",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WalkSchema visited\n%q\nwant\n%q", got, want)
	}
}

// Neither walk walks a kind the schemas do not declare.
func TestWalkUnknownKind(t *testing.T) {
	s := loadWalk(t)
	visited := false
	s.Walk("w.example.org/v1", "Nope", map[string]any{}, func(Node) bool { visited = true; return true })
	s.WalkSchema("w.example.org/v1", "Nope", func(Node) bool { visited = true; return true })
	if visited {
		t.Error("a walk visited an unknown kind")
	}
}

// A node with $ref has the keywords of the schema it refers to with its own laid over them, and
// the annotations of the schema it refers to.
func TestKeywordsBesideRef(t *testing.T) {
	s, err := LoadFiles(map[string][]byte{"k.example.org_v1_K.json": []byte(`{
  "x-chalkos-kind": {"group": "k.example.org", "version": "v1", "kind": "K"},
  "properties": {"x": {"$ref": "#/$defs/X", "description": "Its own."}},
  "$defs": {"X": {"type": "string", "description": "The definition's.", "x-chalkos-networkd-key": "X"}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	s.WalkSchema("k.example.org/v1", "K", func(n Node) bool {
		if n.Path != "x" {
			return true
		}
		kw := n.Keywords()
		if kw["description"] != "Its own." || kw["type"] != "string" || kw["$ref"] != nil {
			t.Errorf("keywords = %v", kw)
		}
		if n.Annotations().NetworkdKey != "X" {
			t.Errorf("annotations = %+v", n.Annotations())
		}
		return true
	})
}
