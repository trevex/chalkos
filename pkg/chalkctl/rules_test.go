package chalkctl

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// greetingSchema is a Greeting kind whose message is refused when longer than 12 characters, or
// changed from the node's, or, by a rule that reads a fact, chalkctl cannot check, never.
const greetingSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "apiVersion": {"const": "greeting.example.org/v1alpha1"},
    "kind": {"const": "Greeting"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}},
    "spec": {"type": "object", "properties": {
      "message": {"type": "string", "x-chalkos-rules": [
        {"rule": "self == oldSelf", "message": "the message is fixed", "takes": "refuse"},
        {"rule": "facts.never", "message": "a fact chalkctl cannot read", "takes": "refuse"}
      ]},
      "loud": {"type": "boolean", "default": false}
    }}
  },
  "x-chalkos-kind": {"group": "greeting.example.org", "version": "v1alpha1", "kind": "Greeting", "multiple": false,
    "rules": [{"rule": "documents.all(n, size(documents[n].spec.message) <= 12)", "message": "a message has at most 12 characters", "takes": "refuse"}]}
}`

func greetings(t *testing.T, text string) []config.Document {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func greeting(message string) string {
	return "apiVersion: greeting.example.org/v1alpha1\nkind: Greeting\nmetadata: {name: hello}\nspec: {message: " + message + "}\n"
}

// chalkctl checks the image's rules that read no facts: without the node's config the kind's,
// and with it, as the node serves it, the field's that compare with it too.
func TestCheckRules(t *testing.T) {
	c, err := newContract(map[string][]byte{"greeting.example.org_v1alpha1_Greeting.json": []byte(greetingSchema)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		next, old string
		want      string
	}{
		{"valid", greeting("hi"), "", ""},
		{"too long", greeting("greetings-to-everyone"), "", "the config is refused: a message has at most 12 characters"},
		{"changed without the node's config", greeting("bye"), "", ""},
		{"changed", greeting("bye"), greeting("hi"), `the config is refused: Greeting "hello" spec.message: the message is fixed`},
		{"unchanged", greeting("hi"), greeting("hi") + "---\napiVersion: gone.example.org/v1\nkind: Gone\nmetadata: {name: x}\nspec: {}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var old []config.Document
			if tc.old != "" {
				old = greetings(t, tc.old)
			}
			next := greetings(t, tc.next)
			filled, err := c.schemas.Default(next[0].Object)
			if err != nil {
				t.Fatal(err)
			}
			next[0].Object = filled.(map[string]any)
			err = checkRules(c, next, old, "n1")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
