package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// kindSchema is the schema of a kind of the group t.example.org with the spec and rules given, as
// modules/node/kinds/schema.nix writes one.
func kindSchema(kind string, multiple bool, spec map[string]any, rules []any) map[string]any {
	xk := map[string]any{"group": "t.example.org", "version": "v1", "kind": kind, "multiple": multiple}
	if rules != nil {
		xk["rules"] = rules
	}
	return map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]any{
			"apiVersion": map[string]any{"const": "t.example.org/v1"},
			"kind":       map[string]any{"const": kind},
			"metadata":   map[string]any{"type": "object"},
			"spec":       spec,
		},
		"x-chalkos-kind": xk,
	}
}

func object(props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props}
}

func withRules(sch map[string]any, rules ...any) map[string]any {
	sch["x-chalkos-rules"] = rules
	return sch
}

func rule(expr, message string, more ...string) map[string]any {
	r := map[string]any{"rule": expr}
	if message != "" {
		r["message"] = message
	}
	for i := 0; i+1 < len(more); i += 2 {
		r[more[i]] = more[i+1]
	}
	return r
}

func schemasOf(t *testing.T, kinds ...map[string]any) *schema.Schemas {
	t.Helper()
	files := map[string][]byte{}
	for _, k := range kinds {
		data, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		files["t.example.org_v1_"+k["x-chalkos-kind"].(map[string]any)["kind"].(string)+".json"] = data
	}
	s, err := schema.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func compiled(t *testing.T, kinds ...map[string]any) *Set {
	t.Helper()
	set, err := Compile(schemasOf(t, kinds...))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// doc is a document of the kind with the spec, given as JSON.
func doc(t *testing.T, kind, name, spec string) config.Document {
	t.Helper()
	var s any
	dec := json.NewDecoder(strings.NewReader(spec))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	return config.FromObject(map[string]any{
		"apiVersion": "t.example.org/v1", "kind": kind,
		"metadata": map[string]any{"name": name}, "spec": s,
	})
}

func messages(failures []Failure) []string {
	var out []string
	for _, f := range failures {
		out = append(out, f.String())
	}
	return out
}

// fakeFacts counts how often each fact is loaded.
type fakeFacts struct {
	values map[string]any
	errs   map[string]error
	loads  map[string]int
}

func (f *fakeFacts) Fact(name string) (any, error) {
	if f.loads == nil {
		f.loads = map[string]int{}
	}
	f.loads[name]++
	return f.values[name], f.errs[name]
}

// A field's rule binds self and oldSelf at its path; one that reads oldSelf runs only where the
// running config holds the field, one that does not wherever the delivered config does. A kind
// with one document compares it with the running one whatever its name.
func TestFieldRules(t *testing.T) {
	spec := object(map[string]any{
		"size":  withRules(map[string]any{"type": "integer"}, rule("self >= oldSelf", "the size only grows", "takes", "reset")),
		"label": withRules(map[string]any{"type": "string"}, rule("self.size() <= 8", "a label has at most 8 characters")),
		"group": object(map[string]any{
			"name": withRules(map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
				rule("self == null || self.startsWith(\"g-\")", "a group's name starts with g-")),
		}),
	})
	set := compiled(t, kindSchema("Demo", true, spec, nil), kindSchema("Solo", false, spec, nil))
	old := []config.Document{
		doc(t, "Demo", "a", `{"size": 2, "label": "a"}`),
		doc(t, "Solo", "before", `{"size": 2}`),
	}
	next := []config.Document{
		doc(t, "Demo", "a", `{"size": 1, "label": "far-too-long", "group": {"name": "x"}}`),
		doc(t, "Demo", "b", `{"size": 1, "label": "b"}`),
		doc(t, "Solo", "after", `{"size": 1}`),
	}
	failures, err := set.Evaluate(Input{Documents: next, Old: old})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Demo "a" spec.group.name: a group's name starts with g-`,
		`Demo "a" spec.label: a label has at most 8 characters`,
		`Demo "a" spec.size: the size only grows`,
		`Solo "after" spec.size: the size only grows`,
	}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if failures[2].Takes != Reset || failures[0].Takes != Refuse {
		t.Errorf("takes = %s and %s, want reset and the default refuse", failures[2].Takes, failures[0].Takes)
	}
	// Without a running config, as at an install, only the rules that read no oldSelf run.
	failures, err = set.Evaluate(Input{Documents: next})
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(failures); !slices.Equal(got, want[:2]) {
		t.Errorf("without a running config: %v", got)
	}
}

// A kind's rules see the documents of the kind by name, and every rule the whole config by
// apiVersion, kind and name, Secret values hashed.
func TestKindRulesAndConfig(t *testing.T) {
	set := compiled(t,
		kindSchema("Demo", true, object(nil), []any{
			rule("size(documents) <= size(oldDocuments) + 1", "", "messageExpression",
				`"add one Demo at a time; this config adds " + documents.filter(n, !(n in oldDocuments)).join(" and ")`, "takes", "reinstall"),
			rule(`documents.all(n, n in config["t.example.org/v1"].Solo.one.spec.allowed)`, "a Demo must be allowed by the Solo"),
		}),
		kindSchema("Solo", false, object(nil), []any{
			rule(`config["chalkos.dev/v1alpha1"].Secret.s.spec.data.token == "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`, "the token is test"),
		}))
	secret := config.FromObject(map[string]any{"apiVersion": config.CoreAPI, "kind": "Secret",
		"metadata": map[string]any{"name": "s"}, "spec": map[string]any{"data": map[string]any{"token": "test"}}})
	docs := []config.Document{
		doc(t, "Demo", "x", `{}`), doc(t, "Demo", "y", `{}`), doc(t, "Demo", "z", `{}`),
		doc(t, "Solo", "one", `{"allowed": ["x", "y"]}`), secret,
	}
	failures, err := set.Evaluate(Input{Documents: docs, Old: docs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"add one Demo at a time; this config adds y and z", "a Demo must be allowed by the Solo"}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures = %q, want %q", got, want)
	}
	if failures[0].Takes != Reinstall {
		t.Errorf("takes = %s", failures[0].Takes)
	}
}

// Every rule and its messageExpression see the node's name as node.name, "" where it is not
// given.
func TestNodeName(t *testing.T) {
	set := compiled(t, kindSchema("Demo", true,
		object(map[string]any{"label": withRules(map[string]any{"type": "string"}, rule(`self != node.name`, "a label is not the node's name"))}),
		[]any{rule(`node.name != "n1"`, "", "messageExpression", `"node " + node.name`)}))
	docs := []config.Document{doc(t, "Demo", "a", `{"label": "n1"}`)}
	failures, err := set.Evaluate(Input{Documents: docs, Node: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node n1", `Demo "a" spec.label: a label is not the node's name`}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures = %q, want %q", got, want)
	}
	set = compiled(t, kindSchema("Demo", true, object(nil), []any{rule(`node.name == ""`, "the node has a name")}))
	if failures, err := set.Evaluate(Input{Documents: docs}); err != nil || len(failures) != 0 {
		t.Errorf("without a name: %v, %v", messages(failures), err)
	}
}

// A rule loads the facts it reads when it reads them, each once per evaluation; without facts,
// as in chalkctl, the rules that read facts are left out. A fact that cannot be loaded refuses.
func TestFacts(t *testing.T) {
	set := compiled(t, kindSchema("Demo", true, object(nil), []any{
		rule(`size(documents) == 0 || facts.limit == null || size(documents) <= facts.limit`, "", "messageExpression",
			`"at most " + string(facts.limit) + " Demos"`),
		rule(`size(documents) < 5 || facts.big`, "five Demos need the big fact"),
		rule(`size(documents) <= 3`, "at most three Demos"),
	}))
	if p := set.Programs[0]; !slices.Equal(p.Facts, []string{"limit"}) {
		t.Errorf("facts = %v", p.Facts)
	}
	docs := []config.Document{doc(t, "Demo", "a", `{}`), doc(t, "Demo", "b", `{}`)}
	f := &fakeFacts{values: map[string]any{"limit": json.Number("1")}}
	failures, err := set.Evaluate(Input{Documents: docs, Facts: f})
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(failures); !slices.Equal(got, []string{"at most 1 Demos"}) {
		t.Errorf("failures = %q", got)
	}
	// big is left unread: the rule's first operand holds.
	if f.loads["big"] != 0 {
		t.Errorf("loads = %v, want big unread", f.loads)
	}
	failures, err = set.Evaluate(Input{Documents: docs})
	if err != nil || len(failures) != 0 {
		t.Errorf("without facts: %v, %v", failures, err)
	}
	f = &fakeFacts{errs: map[string]error{"limit": errors.New("timed out after 30s")}}
	if _, err := set.Evaluate(Input{Documents: docs, Facts: f}); err == nil || err.Error() != "cannot check: fact limit: timed out after 30s" {
		t.Errorf("err = %v", err)
	}
}

// A messageExpression that fails falls back to the rule's message, and without one to a message
// that names the kind or field and what kind of error it is, without the values it read or the
// rule, which can be long.
func TestMessageExpressionFallsBack(t *testing.T) {
	long := strings.Repeat("x", 2000)
	set := compiled(t, kindSchema("Demo", true, object(map[string]any{
		"x": withRules(map[string]any{"type": "object"},
			rule(`self.secret == "`+long+`"`, "", "messageExpression", `string(self.m[self.secret])`),
			rule(`self.secret == "`+long+`"`, "", "messageExpression", `dyn(self.n)`),
			rule(`self.secret == "`+long+`"`, "")),
	}), []any{
		rule("false", "the static message", "messageExpression", `documents.missing`),
		rule(`size(documents) == 0 || documents.a.spec.x.secret == "`+long+`"`, "", "messageExpression",
			`string(documents.a.spec.x.m[documents.a.spec.x.secret])`),
	}))
	failures, err := set.Evaluate(Input{Documents: []config.Document{doc(t, "Demo", "a", `{"x": {"secret": "s3cret", "m": {}, "n": 7777}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"the static message",
		"the config does not meet a rule of Demo, whose messageExpression fails: no such key",
		`Demo "a" spec.x: the config does not meet a rule of Demo spec.x, whose messageExpression fails: no such key`,
		`Demo "a" spec.x: the config does not meet a rule of Demo spec.x, whose messageExpression fails: it yields int, not a string`,
		`Demo "a" spec.x: the config does not meet a rule of Demo spec.x`,
	}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures = %q", got)
	}
	for _, f := range failures {
		if strings.Contains(f.Message, "s3cret") || strings.Contains(f.Message, "7777") || len(f.Message) > 200 {
			t.Errorf("message %q shows a value or the rule", f.Message)
		}
	}
}

// A rule that cannot be evaluated refuses the config with an error that names the field or kind
// that declares the rule and what kind of error it is: neither the rule, which can be long, nor
// the values it read. An error of a kind it does not know says only that the rule cannot be
// evaluated.
func TestEvaluateErrors(t *testing.T) {
	items := make([]string, 2000)
	for i := range items {
		items[i] = `"x"`
	}
	const spec = `{"s": "s3cret", "i": 7777, "z": 0, "max": 9223372036854775807, "m": {}, "l": ["s3cret", 7777], "tz": "Mars/s3cret", "size": "s3cretX"}`
	for name, tc := range map[string]struct {
		rule, spec, want string
	}{
		"no such key":         {`self.m[self.s] == 1`, spec, "a rule cannot be evaluated: no such key"},
		"index out of bounds": {`self.l[self.i] == 1`, spec, "a rule cannot be evaluated: index out of bounds"},
		"index out of range":  {`self.s.charAt(self.i) == ""`, spec, "a rule cannot be evaluated: index out of bounds"},
		"no such overload":    {`self.s + self.i == 1`, spec, "a rule cannot be evaluated: no such overload"},
		"overload signature":  {`sets.contains([1], self.s)`, spec, "a rule cannot be evaluated: no such overload"},
		"unknown time zone":   {`timestamp("2020-01-01T00:00:00Z").getHours(self.tz) == 1`, spec, "a rule cannot be evaluated: unknown time zone"},
		"invalid input":       {`self.l.join(",") == ""`, spec, "a rule cannot be evaluated: invalid input to a function"},
		"cost":                {`self.list.all(a, self.list.all(b, a == b))`, `{"list": [` + strings.Join(items, ",") + `]}`, "a rule cannot be evaluated: cost limit exceeded"},
		"overflow":            {`self.max + self.i == 1`, spec, "a rule cannot be evaluated: overflow"},
		"unsigned overflow":   {`uint(-self.i) == 1u`, spec, "a rule cannot be evaluated: overflow"},
		"division by zero":    {`self.i / self.z == 1`, spec, "a rule cannot be evaluated: division by zero"},
		"modulus by zero":     {`self.i % self.z == 1`, spec, "a rule cannot be evaluated: division by zero"},
		"conversion":          {`int(self.s) > 0`, spec, "a rule cannot be evaluated: conversion error"},
		"timestamp":           {`timestamp(self.s) > timestamp(0)`, spec, "a rule cannot be evaluated: conversion error"},
		"library":             {`bytes(self.size) > 0`, spec, "a rule cannot be evaluated: " + errNotSize},
		"unknown":             {`self.s.matches(self.s + "(")`, spec, "a rule cannot be evaluated"},
		"unknown slice":       {`self.l.slice(0, self.i) == []`, spec, "a rule cannot be evaluated"},
		"not a bool":          {`self.s`, spec, "a rule yields string, not a bool"},
	} {
		t.Run(name, func(t *testing.T) {
			set := compiled(t, kindSchema("Demo", true, object(map[string]any{
				"x": withRules(map[string]any{"type": "object"}, rule(tc.rule, "")),
			}), nil))
			_, err := set.Evaluate(Input{Documents: []config.Document{doc(t, "Demo", "a", `{"x": `+tc.spec+`}`)}})
			if err == nil || err.Error() != `cannot check: Demo "a" spec.x: `+tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			for _, v := range []string{"s3cret", "7777", "9223372036854775807"} {
				if strings.Contains(err.Error(), v) {
					t.Errorf("err = %v shows %s", err, v)
				}
			}
		})
	}
}

// A kind's rule that cannot be evaluated names the kind.
func TestEvaluateKindRuleError(t *testing.T) {
	set := compiled(t, kindSchema("Demo", true, object(nil), []any{rule(`documents.a.spec.missing == 1`, "")}))
	_, err := set.Evaluate(Input{Documents: []config.Document{doc(t, "Demo", "a", `{}`)}})
	if err == nil || err.Error() != "cannot check: Demo: a rule cannot be evaluated: no such key" {
		t.Errorf("err = %v", err)
	}
}

func TestCompileErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		spec  map[string]any
		rules []any
		want  string
	}{
		"syntax":           {object(nil), []any{rule("documents ==", "")}, "Syntax error"},
		"not a bool":       {object(nil), []any{rule("size(documents)", "")}, "yields int, not bool"},
		"message type":     {object(nil), []any{rule("true", "", "messageExpression", "1")}, "messageExpression: yields int, not string"},
		"unknown variable": {object(nil), []any{rule("self == 1", "")}, "undeclared reference to 'self'"},
		"facts by index":   {object(nil), []any{rule(`facts["share"] == null`, "")}, "facts are read by name"},
		"has facts":        {object(nil), []any{rule(`has(facts.share)`, "")}, "has() does not apply to facts"},
		"takes":            {object(nil), []any{rule("true", "", "takes", "wipe")}, `takes "wipe" is none of`},
		"unknown key":      {object(nil), []any{map[string]any{"rule": "true", "mesage": "x"}}, `unknown field "mesage"`},
		"empty":            {object(nil), []any{rule(" ", "")}, "the rule is empty"},
		"costly": {object(nil), []any{rule(
			"documents.all(a, documents.all(b, documents.all(c, a + b + c != \"\")))", "")}, "more than the limit"},
		"below items": {object(map[string]any{"list": map[string]any{"type": "array",
			"items": withRules(map[string]any{"type": "string"}, rule("self != \"\"", ""))}}), nil, "spec.list.*: a rule below a list item"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(schemasOf(t, kindSchema("Demo", true, tc.spec, tc.rules)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A refusal names the most a change takes first, and each rule's message.
func TestRefusal(t *testing.T) {
	reset := Failure{Kind: "Volume", Takes: Reset, Message: "Volume extra: format xfs instead of ext4; reset it"}
	reinstall := Failure{Kind: "Volume", Takes: Reinstall, Message: "Volume data: removed; only a reinstall removes it"}
	refuse := Failure{Kind: "Cluster", Document: "default", Path: []string{"spec", "ipFamilies"}, Takes: Refuse, Message: "fixed"}
	for _, tc := range []struct {
		failures []Failure
		want     string
	}{
		{[]Failure{reset}, "the config takes chalkctl storage reset: Volume extra: format xfs instead of ext4; reset it"},
		{[]Failure{reset, reinstall}, "the config takes a reinstall of n1: Volume extra: format xfs instead of ext4; reset it; Volume data: removed; only a reinstall removes it"},
		{[]Failure{reinstall, refuse}, `the config is refused: Volume data: removed; only a reinstall removes it; Cluster "default" spec.ipFamilies: fixed`},
	} {
		if got := Refusal(tc.failures, "n1"); got != tc.want {
			t.Errorf("Refusal = %q, want %q", got, tc.want)
		}
	}
}

// The running config as a node serves it holds its Secret values hashed already, which rules
// see as they see the delivered config's.
func TestOldHashed(t *testing.T) {
	set := compiled(t, kindSchema("Solo", false, object(nil), []any{
		rule(`config["chalkos.dev/v1alpha1"].Secret.s.spec.data.token == oldConfig["chalkos.dev/v1alpha1"].Secret.s.spec.data.token`, "the token changes"),
	}))
	secret := func(token string) config.Document {
		return config.FromObject(map[string]any{"apiVersion": config.CoreAPI, "kind": "Secret",
			"metadata": map[string]any{"name": "s"}, "spec": map[string]any{"data": map[string]any{"token": token}}})
	}
	served := config.Redact([]config.Document{secret("test")})
	for name, tc := range map[string]struct {
		old    []config.Document
		hashed bool
		want   int
	}{
		"served hashed":     {served, true, 0},
		"as stored":         {[]config.Document{secret("test")}, false, 0},
		"another token":     {config.Redact([]config.Document{secret("other")}), true, 1},
		"hashed twice over": {served, false, 1},
	} {
		failures, err := set.Evaluate(Input{Documents: []config.Document{secret("test")}, Old: tc.old, OldHashed: tc.hashed})
		if err != nil || len(failures) != tc.want {
			t.Errorf("%s: failures = %v, %v; want %d", name, messages(failures), err, tc.want)
		}
	}
}

// addonSchema is the built-in Addon kind, its objects fixed by a rule, and a rule that sees the
// value of the Kubernetes Secrets it applies.
func addonSchema() map[string]any {
	objects := withRules(map[string]any{"type": "array"}, rule("self == oldSelf", "the objects are fixed"))
	sch := kindSchema("Addon", true, object(map[string]any{"objects": objects}), []any{
		rule(`documents.all(n, documents[n].spec.objects[0].data.token != "plain")`, "documents show the token"),
		rule(`config["kubernetes.chalkos.dev/v1alpha1"].Addon.a.spec.objects[0].data.token != "plain"`, "config shows the token"),
	})
	sch["properties"].(map[string]any)["apiVersion"] = map[string]any{"const": config.KubernetesAPI}
	xk := sch["x-chalkos-kind"].(map[string]any)
	xk["group"], xk["version"] = "kubernetes.chalkos.dev", "v1alpha1"
	return sch
}

func addon(token string) config.Document {
	return config.FromObject(map[string]any{"apiVersion": config.KubernetesAPI, "kind": "Addon",
		"metadata": map[string]any{"name": "a"}, "spec": map[string]any{"objects": []any{
			map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "k"},
				"data": map[string]any{"token": token}},
		}}})
}

// Rules see the Kubernetes Secrets an Addon applies with their values hashed, as a node serves
// them, so a rule comparing an Addon with the running one agrees whether the running config is
// the node's own or the one it serves.
func TestAddonSecretsHashed(t *testing.T) {
	data, err := json.Marshal(addonSchema())
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadFiles(map[string][]byte{"kubernetes.chalkos.dev_v1alpha1_Addon.json": data})
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	next := []config.Document{addon("plain")}
	if failures, err := set.Evaluate(Input{Documents: next}); err != nil || len(failures) != 0 {
		t.Errorf("without a running config: failures = %v, %v", messages(failures), err)
	}
	for _, token := range []string{"plain", "other"} {
		stored := []config.Document{addon(token)}
		own, err := set.Evaluate(Input{Documents: next, Old: stored})
		if err != nil {
			t.Fatal(err)
		}
		served, err := set.Evaluate(Input{Documents: next, Old: config.Redact(stored), OldHashed: true})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if token != "plain" {
			want = 1
		}
		if len(own) != want || !slices.Equal(messages(own), messages(served)) {
			t.Errorf("running token %s: the node's own config fails %v, the served one %v; want %d", token, messages(own), messages(served), want)
		}
	}
	if got := ConfigOf(next)[config.KubernetesAPI].(map[string]any)["Addon"].(map[string]any)["a"]; strings.Contains(fmt.Sprint(got), "plain") {
		t.Errorf("ConfigOf shows the token: %v", got)
	}
}

// What a refusal shows of a fact that cannot be loaded is bounded, cut at a character's start.
func TestFactErrorBounded(t *testing.T) {
	err := &FactError{Name: "disks", Err: errors.New(strings.Repeat("é", maxErrorText))}
	got := strings.TrimPrefix(err.Error(), "cannot check: fact disks: ")
	if len(got) > maxErrorText+len("…") || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Errorf("FactError = %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}
