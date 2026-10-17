package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// greeting is a Greeting kind's schema whose field and kind rules are given.
func greeting(fieldRules, kindRules string) string {
	return `{"type": "object", "properties": {"apiVersion": {"const": "greeting.example.org/v1alpha1"},
	  "spec": {"type": "object", "properties": {"message": {"type": "string", "x-chalkos-rules": ` + fieldRules + `}}}},
	  "x-chalkos-kind": {"group": "greeting.example.org", "version": "v1alpha1", "kind": "Greeting", "multiple": false, "rules": ` + kindRules + `}}`
}

// consumer is a consumers.json whose unit greeter restarts and reloads by the rules given.
func consumer(restart, reload string) string {
	return `{"greeter": {"kinds": ["Greeting"], "restart": {"when": "` + restart + `", "start": false},
	  "reload": {"when": "` + reload + `", "command": "systemctl reload greeter"}}}`
}

func dir(t *testing.T, schema, facts, consumers string) (string, string, string) {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "greeting.example.org_v1alpha1_Greeting.json"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(f, []byte(facts), 0o644); err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(t.TempDir(), "consumers.json")
	if err := os.WriteFile(c, []byte(consumers), 0o644); err != nil {
		t.Fatal(err)
	}
	return d, f, c
}

func TestCheck(t *testing.T) {
	const lock = `{"lock": {"file": "/run/lock.json"}, "share": {"file": "/state/share.json"}}`
	fieldRule := `[{"rule": "facts.lock == null || self == oldSelf", "message": "locked"}]`
	kindRule := `[{"rule": "documents.all(n, size(documents[n].spec.message) <= 120)", "message": "too long"}]`
	restarts := consumer(`\"Greeting\" in changedKinds`, `facts.share != oldFacts.share`)
	d, f, c := dir(t, greeting(fieldRule, kindRule), lock, restarts)
	var out bytes.Buffer
	if err := check(d, f, c, &out); err != nil || out.String() != "2 rules and 2 consumer rules compile, reading the facts lock, share\n" {
		t.Errorf("check = %q, %v", out.String(), err)
	}
	for name, tc := range map[string]struct{ schema, facts, consumers, want string }{
		"a rule that does not compile":          {greeting(`[{"rule": "self ==", "message": "x"}]`, kindRule), lock, restarts, "Greeting spec.message: rule \"self ==\""},
		"a misspelt key":                        {greeting(`[{"rule": "true", "mesage": "x"}]`, kindRule), lock, restarts, `unknown field "mesage"`},
		"an undeclared fact":                    {greeting(`[{"rule": "facts.locked == null", "message": "x"}]`, kindRule), lock, restarts, "reads facts.locked, which the image does not declare; it declares lock, share"},
		"a costly rule":                         {greeting(fieldRule, `[{"rule": "documents.all(a, documents.all(b, documents.all(c, a + b + c != \"\")))"}]`), lock, restarts, "more than the limit"},
		"a command outside the store":           {greeting(fieldRule, kindRule), `{"lock": {"command": "/usr/bin/lock"}}`, restarts, "is no program of the image's store"},
		"an unknown input":                      {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "inputs": ["token"]}}`, restarts, `there is no input "token"`},
		"a command that keeps fields":           {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "fields": ["kind"]}}`, restarts, "only a file keeps fields"},
		"a command that hashes":                 {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "hashOthers": true}}`, restarts, "only a file keeps fields or hashes them"},
		"a consumer rule that does not compile": {greeting(fieldRule, kindRule), lock, consumer(`changedKinds ==`, `false`), `greeter restart: rule "changedKinds =="`},
		"a consumer rule of a kind's variable":  {greeting(fieldRule, kindRule), lock, consumer(`false`, `size(documents) > 0`), "undeclared reference to 'documents'"},
		"a consumer rule's undeclared fact":     {greeting(fieldRule, kindRule), lock, consumer(`oldFacts.locked == null`, `false`), `greeter restart: rule "oldFacts.locked == null" reads facts.locked, which the image does not declare`},
	} {
		t.Run(name, func(t *testing.T) {
			d, f, c := dir(t, tc.schema, tc.facts, tc.consumers)
			if err := check(d, f, c, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
