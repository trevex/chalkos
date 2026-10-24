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
			next[0].Object = filled
			err = checkRules(c, next, old, "n1")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// addonSchema is an Addon whose objects are fixed by a rule that compares them with the node's.
const addonSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "apiVersion": {"const": "kubernetes.chalkos.dev/v1alpha1"},
    "kind": {"const": "Addon"},
    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}},
    "spec": {"type": "object", "properties": {
      "objects": {"type": "array", "items": {"type": "object"}, "x-chalkos-rules": [
        {"rule": "self == oldSelf", "message": "the objects are fixed", "takes": "refuse"}
      ]}
    }}
  },
  "x-chalkos-kind": {"group": "kubernetes.chalkos.dev", "version": "v1alpha1", "kind": "Addon", "multiple": true}
}`

func addon(token string) string {
	return "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Addon\nmetadata: {name: a}\nspec:\n  objects:\n" +
		"  - {apiVersion: v1, kind: Secret, metadata: {name: k}, stringData: {token: " + token + "}}\n"
}

// A rule comparing an Addon with the node's, whose Kubernetes Secrets the node serves hashed,
// sees both alike, as the node does.
func TestCheckRulesHashesAddonSecrets(t *testing.T) {
	c, err := newContract(map[string][]byte{"kubernetes.chalkos.dev_v1alpha1_Addon.json": []byte(addonSchema)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		node, want string
	}{
		{"test", ""},
		{"other", `the config is refused: Addon "a" spec.objects: the objects are fixed`},
	} {
		served := config.Redact(greetings(t, addon(tc.node)))
		err := checkRules(c, greetings(t, addon("test")), served, "n1")
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
			t.Errorf("node token %s: err = %v, want %q", tc.node, err, tc.want)
		}
	}
}
