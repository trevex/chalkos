package derive

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// array is the documents of byKind as a validator reads them: one JSON array, sorted by kind and
// then by name.
func array(t *testing.T, byKind map[string]any) []byte {
	t.Helper()
	var docs []any
	for _, kind := range Kinds {
		list, _ := byKind[kind].([]any)
		docs = append(docs, list...)
	}
	data, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The validator reads the array of documents as Build takes an output's input, so the model of
// the array is the renderers' model, and refuses none of the examples.
func TestValidate(t *testing.T) {
	for _, name := range []string{"vrf", "policy", "nat64", "dhcp-relay"} {
		input := example(t, name)
		docs := array(t, input)
		if rs, err := Validate(docs); err != nil || len(rs) != 0 {
			t.Errorf("Validate(%s) = %v, %v, want no refusals", name, rs, err)
		}
		read, err := readDocuments(docs)
		if err != nil {
			t.Fatal(err)
		}
		fromArray, err := Build(read)
		if err != nil {
			t.Fatal(err)
		}
		fromInput, err := Build(input)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromArray, fromInput) {
			t.Errorf("%s: the model of the array differs from the model of the output's input", name)
		}
	}
	if rs, err := Validate([]byte("[]")); err != nil || len(rs) != 0 {
		t.Errorf("Validate([]) = %v, %v", rs, err)
	}
	for input, want := range map[string]string{
		`{"kind": "VRF"}`:          "the documents are no JSON array of documents",
		`[{"kind": "VRF"}] []`:     "more follows the array",
		`[{"metadata": {}}]`:       "document 1 names no kind",
		`[{"kind": "VRF"}, "VRF"]`: "the documents are no JSON array of documents",
	} {
		if _, err := Validate([]byte(input)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%s) = %v, want %q", input, err, want)
		}
	}
}
