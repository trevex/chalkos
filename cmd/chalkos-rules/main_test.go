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

func dir(t *testing.T, schema, facts string) (string, string) {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "greeting.example.org_v1alpha1_Greeting.json"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(f, []byte(facts), 0o644); err != nil {
		t.Fatal(err)
	}
	return d, f
}

func TestCheck(t *testing.T) {
	const lock = `{"lock": {"file": "/run/lock.json"}}`
	fieldRule := `[{"rule": "facts.lock == null || self == oldSelf", "message": "locked"}]`
	kindRule := `[{"rule": "documents.all(n, size(documents[n].spec.message) <= 120)", "message": "too long"}]`
	d, f := dir(t, greeting(fieldRule, kindRule), lock)
	var out bytes.Buffer
	if err := check(d, f, &out); err != nil || out.String() != "2 rules compile, reading the facts lock\n" {
		t.Errorf("check = %q, %v", out.String(), err)
	}
	for name, tc := range map[string]struct{ schema, facts, want string }{
		"a rule that does not compile": {greeting(`[{"rule": "self ==", "message": "x"}]`, kindRule), lock, "Greeting spec.message: rule \"self ==\""},
		"a misspelt key":               {greeting(`[{"rule": "true", "mesage": "x"}]`, kindRule), lock, `unknown field "mesage"`},
		"an undeclared fact":           {greeting(`[{"rule": "facts.locked == null", "message": "x"}]`, kindRule), lock, "reads facts.locked, which the image does not declare; it declares lock"},
		"a costly rule":                {greeting(fieldRule, `[{"rule": "documents.all(a, documents.all(b, documents.all(c, a + b + c != \"\")))"}]`), lock, "more than the limit"},
		"a command outside the store":  {greeting(fieldRule, kindRule), `{"lock": {"command": "/usr/bin/lock"}}`, "is no program of the image's store"},
		"an unknown input":             {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "inputs": ["token"]}}`, `there is no input "token"`},
		"a command that keeps fields":  {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "fields": ["kind"]}}`, "only a file keeps fields"},
	} {
		t.Run(name, func(t *testing.T) {
			d, f := dir(t, tc.schema, tc.facts)
			if err := check(d, f, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
