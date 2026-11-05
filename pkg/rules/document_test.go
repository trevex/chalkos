package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// laneSpec is the spec of Lane, the kind Zone's rules look documents up of.
var laneSpec = object(map[string]any{
	"family": map[string]any{"type": "string"},
	"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
})

func nullable(t string) map[string]any {
	return map[string]any{"anyOf": []any{map[string]any{"type": t}, map[string]any{"type": "null"}}}
}

// document() finds a document of another kind by name, as config holds it, and null where config
// holds none or the name is null, so a rule reads the target's fields with one lookup.
func TestDocument(t *testing.T) {
	zone := object(map[string]any{
		"lane": withRules(nullable("string"),
			rule(`document("Lane", self) == null || document("Lane", self).spec.family == "ipv4"`, "the lane is not of ipv4")),
		"lanes": withRules(map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			rule(`self.all(l, document("Lane", l) != null)`, "a lane does not exist")),
		"same": withRules(map[string]any{"type": "string"},
			rule(`document("Lane", self) == config["t.example.org/v1"].Lane[self]`, "document() is not config's")),
	})
	set := compiled(t, kindSchema("Lane", true, laneSpec, nil), kindSchema("Zone", true, zone, nil))
	docs := []config.Document{
		doc(t, "Lane", "a", `{"family": "ipv4"}`),
		doc(t, "Lane", "b", `{"family": "ipv6"}`),
		doc(t, "Zone", "ok", `{"lane": "a", "lanes": ["a", "b"], "same": "a"}`),
		doc(t, "Zone", "none", `{"lane": null, "lanes": [], "same": "b"}`),
		doc(t, "Zone", "missing", `{"lane": "c", "lanes": ["c"], "same": "a"}`),
		doc(t, "Zone", "v6", `{"lane": "b", "lanes": [], "same": "a"}`),
	}
	failures, err := set.Evaluate(Input{Documents: docs})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Zone/v6: spec.lane: the lane is not of ipv4`,
		`Zone/missing: spec.lanes: a lane does not exist`,
	}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures = %q, want %q", got, want)
	}
}

// The kind is a string literal of a kind the image declares, or the rule does not compile, which
// fails the image's build.
func TestDocumentKindMustBeDeclared(t *testing.T) {
	for expr, want := range map[string]string{
		`document("Nope", self) != null`: "document: the image declares no kind Nope; it declares Lane, Zone",
		`document(self, self) != null`:   `document: the kind is a string literal, such as "VRF"`,
	} {
		zone := object(map[string]any{"lane": withRules(map[string]any{"type": "string"}, rule(expr, "x"))})
		_, err := Compile(schemasOf(t, kindSchema("Lane", true, laneSpec, nil), kindSchema("Zone", true, zone, nil)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want one saying %q", expr, err, want)
		}
	}
}

// A lookup costs as one step: a loop over a list that looks each item up and loops over the
// target's list costs as two nested loops, which fit, and a third loop inside does not.
func TestDocumentCostsOneLookup(t *testing.T) {
	for expr, fits := range map[string]bool{
		`self.all(l, document("Lane", l).spec.tags.all(t, t != ""))`:             true,
		`self.all(l, document("Lane", l).spec.tags.all(t, self.all(u, u != t)))`: false,
	} {
		zone := object(map[string]any{"lanes": withRules(map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, rule(expr, "x"))})
		_, err := Compile(schemasOf(t, kindSchema("Lane", true, laneSpec, nil), kindSchema("Zone", true, zone, nil)))
		if fits && err != nil {
			t.Errorf("%s: %v", expr, err)
		}
		if !fits && (err == nil || !strings.Contains(err.Error(), "more than the limit")) {
			t.Errorf("%s: err = %v, want the cost limit", expr, err)
		}
	}
}

// document() reads the config whatever a rule names its own variables, so a loop or cel.bind
// variable named config does not take the place of the config it looks documents up in.
func TestDocumentIgnoresShadowedConfig(t *testing.T) {
	zone := object(map[string]any{
		"lanes": withRules(map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			rule(`self.all(config, document("Lane", config) != null)`, "a lane does not exist"),
			rule(`cel.bind(config, {}, self.all(l, document("Lane", l) != null))`, "a bound lane does not exist")),
	})
	set := compiled(t, kindSchema("Lane", true, laneSpec, nil), kindSchema("Zone", true, zone, nil))
	docs := []config.Document{
		doc(t, "Lane", "a", `{"family": "ipv4"}`),
		doc(t, "Zone", "ok", `{"lanes": ["a"]}`),
		doc(t, "Zone", "missing", `{"lanes": ["c"]}`),
	}
	failures, err := set.Evaluate(Input{Documents: docs})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Zone/missing: spec.lanes: a lane does not exist`,
		`Zone/missing: spec.lanes: a bound lane does not exist`,
	}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures = %q, want %q", got, want)
	}
}

// document("Secret", name) holds the Secret's values hashed, as config does, never the values.
func TestDocumentHashesSecrets(t *testing.T) {
	secretSchema := kindSchema("Secret", true, object(nil), nil)
	secretSchema["properties"].(map[string]any)["apiVersion"] = map[string]any{"const": config.CoreAPI}
	secretSchema["x-chalkos-kind"].(map[string]any)["group"] = "chalkos.dev"
	secretSchema["x-chalkos-kind"].(map[string]any)["version"] = "v1alpha1"
	zone := object(map[string]any{
		"secret": withRules(map[string]any{"type": "string"},
			rule(`document("Secret", self).spec.data.token != "test"`, "the token is plain"),
			rule(`document("Secret", self).spec.data.token == "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`, "the token is not hashed")),
	})
	set := compiled(t, secretSchema, kindSchema("Zone", true, zone, nil))
	docs := []config.Document{
		config.FromObject(map[string]any{"apiVersion": config.CoreAPI, "kind": "Secret",
			"metadata": map[string]any{"name": "s"}, "spec": map[string]any{"data": map[string]any{"token": "test"}}}),
		doc(t, "Zone", "z", `{"secret": "s"}`),
	}
	failures, err := set.Evaluate(Input{Documents: docs})
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(failures); len(got) != 0 {
		t.Errorf("failures = %q, want none", got)
	}
}
