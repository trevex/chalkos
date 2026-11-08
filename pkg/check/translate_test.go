package check

import (
	"errors"
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

// TranslateRedacted gives a redacted set the documents and identity of the set with its values,
// redacted: the defaults it fills in hide nothing again.
func TestTranslateRedacted(t *testing.T) {
	img := Image{Schemas: testSchemas(t), Target: config.Target{Platform: "metal"}}
	docs := parse(t, base)
	raw, err := img.Translate("n1", docs)
	if err != nil {
		t.Fatal(err)
	}
	redacted, id, err := img.TranslateRedacted("n1", config.Redact(docs))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(redacted.Objects(), config.Redact(raw.Documents).Objects()) || !reflect.DeepEqual(id, raw.Identity) {
		t.Errorf("redacted %v, %+v; values %v, %+v", redacted.Objects(), id, raw.Documents, raw.Identity)
	}
}

// A Secret value that is no string fails the schema in its redacted form as it does with its
// value, with the same refusal, so the image's checker, which sees redacted documents alone,
// refuses it.
func TestTranslateRedactedRefusesAMistypedSecret(t *testing.T) {
	img := Image{Schemas: testSchemas(t), Target: config.Target{Platform: "metal"}}
	for _, v := range []string{"12345", "true", "[a]", "{a: b}", "null"} {
		docs := parse(t, strings.Replace(base, "token: s3cret", "token: "+v, 1))
		_, raw := img.Translate("n1", docs)
		_, _, redacted := img.TranslateRedacted("n1", config.Redact(docs))
		r, ok := errors.AsType[*Refusal](raw)
		if !ok || r.Stage != StageConfig || redacted == nil || raw.Error() != redacted.Error() {
			t.Errorf("%s: with its value %v, redacted %v", v, raw, redacted)
		}
	}
}
