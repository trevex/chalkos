package derive

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// stdin is the documents of byKind as a validator reads them: the node's name and the documents
// of each kind, as a renderer reads them.
func stdin(t *testing.T, byKind any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"node": map[string]any{"name": "r1"}, "documents": byKind})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The validator reads the input a renderer reads, so the model it checks is the renderers'
// model, and refuses none of the examples.
func TestValidate(t *testing.T) {
	for _, name := range []string{"vrf", "policy", "nat64", "dhcp-relay"} {
		input := example(t, name)
		data := stdin(t, input)
		if rs, err := Validate(data); err != nil || len(rs) != 0 {
			t.Errorf("Validate(%s) = %v, %v, want no refusals", name, rs, err)
		}
		read, err := readDocuments(data)
		if err != nil {
			t.Fatal(err)
		}
		fromStdin, err := Build(read)
		if err != nil {
			t.Fatal(err)
		}
		fromInput, err := Build(input)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromStdin, fromInput) {
			t.Errorf("%s: the model of the validator's input differs from the model of the output's input", name)
		}
	}
	if rs, err := Validate(stdin(t, map[string]any{})); err != nil || len(rs) != 0 {
		t.Errorf("Validate of no documents = %v, %v", rs, err)
	}
	for input, want := range map[string]string{
		`[{"kind": "VRF"}]`:        "the input is no object of documents by kind",
		`{"documents": {}} {}`:     "more follows the object",
		`{"node": {"name": "r1"}}`: "the input holds no documents",
	} {
		if _, err := Validate([]byte(input)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%s) = %v, want %q", input, err, want)
		}
	}
}
