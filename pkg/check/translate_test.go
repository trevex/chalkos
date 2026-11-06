package check

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
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
	return schemasWith(t)
}

// schemasWith are testSchemas with each pair of old and new strings replaced once in the Demo
// kind's schema.
func schemasWith(t *testing.T, replace ...string) *schema.Schemas {
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
	for i := 0; i+1 < len(replace); i += 2 {
		if !strings.Contains(string(files[demo]), replace[i]) {
			t.Fatalf("the Demo schema lacks %s", replace[i])
		}
		files[demo] = []byte(strings.Replace(string(files[demo]), replace[i], replace[i+1], 1))
	}
	s, err := schema.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// facts are a node's facts by name, which record the names asked for; err, when set, is the
// error of every fact.
type facts struct {
	values map[string]any
	asked  []string
	err    error
}

func (f *facts) Fact(name string) (any, error) {
	f.asked = append(f.asked, name)
	return f.values[name], f.err
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

// A Secret value that is no string fails the schema in its hashed form as it does with its value,
// with the same message, so the image's checker, which sees hashed documents alone, refuses it.
func TestTranslateHashedRefusesAMistypedSecret(t *testing.T) {
	s := testSchemas(t)
	for _, v := range []string{"12345", "true", "[a]", "{a: b}", "null"} {
		docs := parse(t, strings.Replace(base, "token: s3cret", "token: "+v, 1))
		_, raw := Translate("n1", docs, s, config.Target{Platform: "metal"})
		_, hashed := Translate("n1", config.Redact(docs), s, config.Target{Platform: "metal"})
		if raw == nil || hashed == nil || raw.Error() != hashed.Error() {
			t.Errorf("%s: with its value %v, hashed %v", v, raw, hashed)
		}
	}
}
