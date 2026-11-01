package check

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// base is a node's config: its system disk and a Demo whose token is a Secret's.
const base = `
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec: {disk: {path: /dev/vda}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

// frozenRule keeps a Demo's name while the fact frozen is true.
const frozenRule = `"x-chalkos-rules": [{"rule": "facts.frozen != true || self == oldSelf", "message": "the name stays while the demo is frozen"}],`

// testSchemas are the built-in kinds' schemas and the Demo kind's, whose name keeps frozenRule.
func testSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	files := map[string][]byte{}
	for _, dir := range []string{"../../test/fixtures/kinds/builtin", "../../test/fixtures/kinds/image/schemas"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = data
		}
	}
	demo := "demo.example.org_v1alpha1_Demo.json"
	files[demo] = []byte(strings.Replace(string(files[demo]), `"name": { "type": "string", "description"`, `"name": { "type": "string", `+frozenRule+` "description"`, 1))
	s, err := schema.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// facts are a node's facts by name, which record the names asked for.
type facts struct {
	values map[string]any
	asked  []string
}

func (f *facts) Fact(name string) (any, error) {
	f.asked = append(f.asked, name)
	return f.values[name], nil
}

// parse parses YAML config documents.
func parse(t *testing.T, text string) []config.Document {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// Translate gives a set whose secret values are hashed the documents and identity of the set with
// its values, with the hashes in place of the values.
func TestTranslateHashed(t *testing.T) {
	s := testSchemas(t)
	docs := parse(t, base)
	raw, err := Translate("n1", docs, s, config.Target{Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	hashed, err := Translate("n1", config.Redact(docs), s, config.Target{Platform: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hashed.Documents, config.Documents(config.Redact(raw.Documents))) || string(hashed.Identity) != string(raw.Identity) {
		t.Errorf("hashed %v, %s; values %v, %s", hashed.Documents, hashed.Identity, raw.Documents, raw.Identity)
	}
}

// Rules refuses a change a rule refuses on the node's facts, and lets the same change pass on
// other facts.
func TestRules(t *testing.T) {
	s := testSchemas(t)
	set, err := rules.Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	translate := func(text string) config.Documents {
		tr, err := Translate("n1", config.Redact(parse(t, text)), s, config.Target{Platform: "metal"})
		if err != nil {
			t.Fatal(err)
		}
		return tr.Documents
	}
	old, next := translate(base), translate(strings.Replace(base, "name: first", "name: second", 1))
	for _, frozen := range []bool{false, true} {
		failures, err := Rules(set, "n1", next, old, &facts{values: map[string]any{"frozen": frozen}})
		if err != nil || (len(failures) == 1) != frozen {
			t.Errorf("frozen %v: failures %v, %v", frozen, failures, err)
		}
	}
}
