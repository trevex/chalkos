package schema

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestLoadFiles(t *testing.T) {
	entries, err := os.ReadDir(builtin)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(builtin, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = data
	}
	s, err := LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Kinds()) != len(files) {
		t.Errorf("kinds = %v, want one per file of %d", s.Kinds(), len(files))
	}
	for _, name := range []string{"../x.json", ".x.json", "x.yaml", "a/x.json"} {
		_, err := LoadFiles(map[string][]byte{name: []byte("{}")})
		if err == nil || !strings.Contains(err.Error(), "is not the name of a schema file") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// kindSchema is the schema file of the kind Probe of example.org/v1 with the schema's other
// keywords body, a JSON object's members.
func kindSchema(body string) map[string][]byte {
	return map[string][]byte{"example.org_v1_Probe.json": []byte(`{"x-chalkos-kind": {"group": "example.org", "version": "v1", "kind": "Probe", "multiple": false}, ` + body + `}`)}
}

// TestSchemasFetchNothing: a schema refers to nothing outside the image's schemas, so a file on
// the machine, a relative reference to a schema that is not there and a URL all fail alike, and
// the error says nothing of a file's content or whether it exists.
func TestSchemasFetchNothing(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"type": "string", "description": "zq-content-7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	errOf := func(ref string) string {
		_, err := LoadFiles(kindSchema(`"$ref": "` + ref + `"`))
		var lerr *LoadError
		if !errors.As(err, &lerr) || lerr.File != "example.org_v1_Probe.json" {
			t.Fatalf("%s: err = %v, want a LoadError naming the file", ref, err)
		}
		if !strings.Contains(err.Error(), "may refer only to the image's schemas") {
			t.Errorf("%s: err = %v", ref, err)
		}
		// The temporary directory LoadFiles writes to differs from call to call.
		_, msg, _ := strings.Cut(err.Error(), ".json: ")
		return strings.ReplaceAll(msg, ref, "REF")
	}
	present, absent := errOf("file://"+secret), errOf("file://"+missing)
	if strings.Contains(present, "zq-content-7") || present != absent {
		t.Errorf("a file that exists fails as\n%s\nand one that does not as\n%s", present, absent)
	}
	errOf("http://example.org/schema.json")
	errOf("other.json")
}

// selfReferring are schemas of the kind Probe whose spec refers to itself: a group of options
// holding itself, a $ref cycle and an anyOf holding itself.
var selfReferring = map[string]string{
	"a group holding itself": `"type": "object", "properties": {"spec": {"$ref": "#/$defs/g"}},
		"$defs": {"g": {"type": "object", "properties": {"spec": {"$ref": "#/$defs/g"}}}}`,
	"a $ref cycle": `"type": "object", "properties": {"spec": {"$ref": "#/$defs/a"}},
		"$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}`,
	"an anyOf holding itself": `"type": "object", "properties": {"spec": {"$ref": "#/$defs/a"}},
		"$defs": {"a": {"anyOf": [{"$ref": "#/$defs/a"}]}}`,
}

// chain is a schema of the kind Probe whose spec refers to the first of n definitions, each of
// which refers to the next from two places, the way of, and the last of which is last.
func chain(n int, way, last string) string {
	var defs []string
	for i := range n {
		next := fmt.Sprintf(`{"$ref": "#/$defs/d%d"}`, i+1)
		defs = append(defs, fmt.Sprintf(`"d%d": `+way, i, next, next))
	}
	defs = append(defs, fmt.Sprintf(`"d%d": %s`, n, last))
	return `"type": "object", "properties": {"spec": {"$ref": "#/$defs/d0"}}, "$defs": {` + strings.Join(defs, ", ") + `}`
}

// The validation bomb: every definition holds the next twice in an allOf. The defaults bomb: every
// definition is a group of options a and b, both the next, and the last has a default.
var (
	validationBomb = chain(30, `{"allOf": [%s, %s]}`, `{"type": "string"}`)
	defaultsBomb   = chain(20, `{"type": "object", "properties": {"a": %s, "b": %s}}`, `{"type": "string", "default": "x"}`)
)

// within fails the test unless f returns within ten seconds.
func within(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("did not end within ten seconds")
	}
}

// TestLoadRefusesSchemasOfUnboundedWork: a schema that refers to itself, or expands through its
// references to more schemas than any chalkos kind, is refused when it loads, quickly, before
// a document is validated against it or filled in.
func TestLoadRefusesSchemasOfUnboundedWork(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"the validation bomb": {validationBomb, "expands to more than"},
		"the defaults bomb":   {defaultsBomb, "expands to more than"},
	}
	for name, body := range selfReferring {
		cases[name] = struct{ body, want string }{body, "refers to itself"}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			within(t, func() {
				_, err := LoadFiles(kindSchema(tc.body))
				var lerr *LoadError
				if !errors.As(err, &lerr) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("err = %v, want a LoadError saying %q", err, tc.want)
				}
			})
		})
	}
}

// uncheckedSchemas compiles the kind Probe's schema without the checks LoadSchemas makes, for the
// guards Default keeps of its own.
func uncheckedSchemas(t *testing.T, body string) *Schemas {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(noLoader{})
	if err := c.AddResource("chalkos:///schemas/probe.json", decode(t, "{"+body+"}")); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("chalkos:///schemas/probe.json")
	if err != nil {
		t.Fatal(err)
	}
	return &Schemas{schemas: map[Kind]*jsonschema.Schema{key("example.org/v1", "Probe"): sch}}
}

// TestDefaultEndsOnItsOwn: Default refuses, quickly and in little memory, a schema that refers to
// itself or whose groups of options expand without bound, should one reach it unchecked.
func TestDefaultEndsOnItsOwn(t *testing.T) {
	cases := map[string]string{"the defaults bomb": defaultsBomb}
	for name, body := range selfReferring {
		cases[name] = body
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := uncheckedSchemas(t, body)
			within(t, func() {
				doc := map[string]any{"apiVersion": "example.org/v1", "kind": "Probe", "metadata": map[string]any{"name": "p"}, "spec": map[string]any{}}
				if _, err := s.Default(doc); err == nil {
					t.Error("Default filled in a schema of unbounded work")
				}
			})
		})
	}
}

// TestLoadRefusesASchemaTheLibraryPanicsOn: a multipleOf too small for math/big makes the
// library panic as it compiles; that is a LoadError, not a crash.
func TestLoadRefusesASchemaTheLibraryPanicsOn(t *testing.T) {
	_, err := LoadFiles(kindSchema(`"type": "object", "properties": {"n": {"type": "number", "multipleOf": 1e-5000000}}`))
	var lerr *LoadError
	if !errors.As(err, &lerr) || !strings.Contains(err.Error(), "does not compile") {
		t.Fatalf("err = %v, want a LoadError for the library's panic", err)
	}
}
