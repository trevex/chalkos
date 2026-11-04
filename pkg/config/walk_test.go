package config

import (
	"reflect"
	"testing"
)

// walk reaches every node the schema describes, a nullable field's node before its branch with the
// same paths, and names list items with [] and map values with [*] in the schema's path, which is
// the same in every document; it skips below a node whose visit returns false, and leaves a key
// the schema does not describe alone.
func TestWalk(t *testing.T) {
	sch := map[string]any{"type": "object", "properties": map[string]any{
		"skip": map[string]any{"type": "object", "properties": map[string]any{"inner": map[string]any{"type": "string"}}},
		"rules": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
			"to": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
		}}},
		"byName": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}},
	}}
	v := map[string]any{
		"skip":    map[string]any{"inner": "x"},
		"rules":   []any{map[string]any{"to": "a"}, map[string]any{"to": nil}},
		"byName":  map[string]any{"a.b": 1},
		"unknown": "y",
	}
	var got [][2]string
	walk(nil, node{sch: sch, v: v, path: "spec", field: "spec"}, func(n node) bool {
		got = append(got, [2]string{n.path, n.field})
		return n.path != "spec.skip"
	})
	want := [][2]string{
		{"spec", "spec"},
		{"spec.byName", "spec.byName"},
		{`spec.byName["a.b"]`, "spec.byName[*]"},
		{"spec.rules", "spec.rules"},
		{"spec.rules[0]", "spec.rules[]"},
		{"spec.rules[0].to", "spec.rules[].to"},
		{"spec.rules[0].to", "spec.rules[].to"},
		{"spec.rules[1]", "spec.rules[]"},
		{"spec.rules[1].to", "spec.rules[].to"},
		{"spec.rules[1].to", "spec.rules[].to"},
		{"spec.skip", "spec.skip"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("walk visited\n%v\nwant\n%v", got, want)
	}
}
