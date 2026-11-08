package main

import (
	"bytes"
	"os"
	"os/exec"
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

// effect is an effects.json whose effect greeter restarts greeter.service when the rule given
// holds, and reloads it when the second holds.
func effect(restart, reload string) string {
	return `{"greeter": {"when": "` + restart + `", "restart": ["greeter.service"]},
	  "greeter-reload": {"when": "` + reload + `", "reload": ["greeter.service"]}}`
}

// image lays out an image's files as its build does, with the unit greeter.service.
func image(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, content := range files {
		path := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func files(schema, facts, effects string) map[string]string {
	return map[string]string{
		"schemas/greeting.example.org_v1alpha1_Greeting.json": schema,
		"facts.json":              facts,
		"effects.json":            effects,
		"units/greeter.service":   "[Service]\n",
		"units/multi-user.target": "",
	}
}

const (
	lock      = `{"lock": {"file": "/run/lock.json"}, "share": {"file": "/state/share.json"}}`
	fieldRule = `[{"rule": "facts.lock == null || self == oldSelf", "message": "locked"}]`
	kindRule  = `[{"rule": "documents.all(n, size(documents[n].spec.message) <= 120)", "message": "too long"}]`
)

func TestCheck(t *testing.T) {
	restarts := effect(`node.name == \"n1\"`, `facts.share != oldFacts.share`)
	var out bytes.Buffer
	if err := check(image(t, files(greeting(fieldRule, kindRule), lock, restarts)), &out); err != nil || out.String() != "2 rules and 2 effect rules compile, reading the facts lock, share; 0 outputs render 0 examples; 0 validators accept 0 examples\n" {
		t.Errorf("check = %q, %v", out.String(), err)
	}
	for name, tc := range map[string]struct{ schema, facts, effects, want string }{
		"a rule that does not compile":         {greeting(`[{"rule": "self ==", "message": "x"}]`, kindRule), lock, restarts, "Greeting spec.message: rule \"self ==\""},
		"a misspelt key":                       {greeting(`[{"rule": "true", "mesage": "x"}]`, kindRule), lock, restarts, `unknown field "mesage"`},
		"an undeclared fact":                   {greeting(`[{"rule": "facts.locked == null", "message": "x"}]`, kindRule), lock, restarts, "reads facts.locked, which the image does not declare; it declares lock, share"},
		"a costly rule":                        {greeting(fieldRule, `[{"rule": "documents.all(a, documents.all(b, documents.all(c, a + b + c != \"\")))"}]`), lock, restarts, "more than the limit"},
		"a command outside the store":          {greeting(fieldRule, kindRule), `{"lock": {"command": "/usr/bin/lock"}}`, restarts, "is no program of the image's store"},
		"an unknown input":                     {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "inputs": ["token"]}}`, restarts, `there is no input "token"`},
		"a command that keeps fields":          {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "fields": ["kind"]}}`, restarts, "only a file keeps fields"},
		"a command that hashes":                {greeting(fieldRule, kindRule), `{"lock": {"command": "/nix/store/x/bin/lock", "hashOthers": true}}`, restarts, "only a file keeps fields or hashes them"},
		"an effect rule that does not compile": {greeting(fieldRule, kindRule), lock, effect(`config ==`, `false`), `effect greeter: rule "config =="`},
		"an effect rule of a kind's variable":  {greeting(fieldRule, kindRule), lock, effect(`false`, `size(documents) > 0`), "undeclared reference to 'documents'"},
		"an effect rule's undeclared fact":     {greeting(fieldRule, kindRule), lock, effect(`oldFacts.locked == null`, `false`), `effect greeter: rule "oldFacts.locked == null" reads facts.locked, which the image does not declare`},
		"an undefined unit":                    {greeting(fieldRule, kindRule), lock, `{"e": {"when": "true", "restart": ["bird.service"]}}`, "effect e: the image defines no unit bird.service"},
		"a unit without its suffix":            {greeting(fieldRule, kindRule), lock, `{"e": {"when": "true", "reload": ["greeter"]}}`, `"greeter" is no unit name, such as bird.service`},
		"an empty command":                     {greeting(fieldRule, kindRule), lock, `{"e": {"when": "true", "run": " "}}`, "effect e: the command to run is empty"},
		"an effect that does nothing":          {greeting(fieldRule, kindRule), lock, `{"e": {"when": "true"}}`, "effect e: it restarts, reloads and runs nothing"},
		"an effect nothing triggers":           {greeting(fieldRule, kindRule), lock, `{"e": {"restart": ["greeter.service"]}}`, "effect e: it has none of outputs, kinds and when"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(image(t, files(tc.schema, tc.facts, tc.effects)), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A diagnostic chalkd would refuse fails the check, as it would fail chalkd's reading of it.
func TestCheckRefusesADiagnostic(t *testing.T) {
	f := files(greeting(fieldRule, kindRule), lock, effect(`false`, `false`))
	f["diagnostics.json"] = `{"route": {"description": "A route.", "role": "reader", "timeout": "10s",
	  "command": ["/nix/store/x-iproute2/bin/ip", "route", "show", "table", "{table}"],
	  "parameters": {"table": {"type": "pattern", "description": "A table.", "regex": "^[a-z$"}}}}`
	err := check(image(t, f), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "diagnostic route: parameter table: its regex: error parsing regexp") {
		t.Errorf("err = %v, want one naming the diagnostic's regex", err)
	}
}

// A services.json chalkd could not read fails the check, as it would stop chalkd from starting.
func TestCheckRefusesServices(t *testing.T) {
	f := files(greeting(fieldRule, kindRule), lock, effect(`false`, `false`))
	f["services.json"] = `{"greeter.service": {"kinds": "Greeting"}}`
	err := check(image(t, f), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "services.json") {
		t.Errorf("err = %v, want one naming services.json", err)
	}
}

// storeShell is the shell of the image's store that runs the test's scripts, as the renderer of
// an image runs from its store.
func storeShell(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err == nil {
		sh, err = filepath.EvalSymlinks(sh)
	}
	if err != nil || !strings.HasPrefix(sh, "/nix/store/") {
		t.Skipf("no shell of the store: %s, %v", sh, err)
	}
	return sh
}

// withOutput adds to files the output greeting, which renders the Greeting's message by the shell
// commands given and checks it by check, with the examples given, and the effect greeter on it.
func withOutput(t *testing.T, files map[string]string, render, check string, examples map[string]string) map[string]string {
	t.Helper()
	sh := storeShell(t)
	scripts := t.TempDir()
	os.WriteFile(filepath.Join(scripts, "render"), []byte("set -e\n"+render+"\n"), 0o644)
	os.WriteFile(filepath.Join(scripts, "check"), []byte("set -e\n"+check+"\n"), 0o644)
	files["outputs.json"] = `{"greeting": {"inputs": ["Greeting"], "render": "` + sh + " " + filepath.Join(scripts, "render") + `",
	  "check": "` + sh + " " + filepath.Join(scripts, "check") + `", "dir": "/run/chalkos/outputs/greeting", "user": "nobody", "group": "nobody", "mode": "0644"}}`
	files["effects.json"] = `{"greeter": {"outputs": ["greeting"], "restart": ["greeter.service"]}}`
	files["examples.json"] = `{"outputs": {"greeting": "` + filepath.Join(scripts, "examples") + `"}}`
	for name, text := range examples {
		path := filepath.Join(scripts, "examples", name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(text), 0o644)
	}
	return files
}

const hello = "apiVersion: greeting.example.org/v1alpha1\nkind: Greeting\nmetadata: {name: g}\nspec: {message: hello}\n"

// The image's build renders each example of each output and runs its check, and refuses an
// output it could not run on a node and an effect on an output the image lacks.
func TestCheckOutputs(t *testing.T) {
	render := `sed -n 's/.*"message":"\([^"]*\)".*/\1/p' > "$OUT/message"`
	accepts := `grep -qx hello "$OUT/message"`
	base := func() map[string]string { return files(greeting(fieldRule, kindRule), lock, "{}") }
	var out bytes.Buffer
	if err := check(image(t, withOutput(t, base(), render, accepts, map[string]string{"hello.yaml": hello})), &out); err != nil || !strings.HasSuffix(out.String(), "; 1 outputs render 1 examples; 0 validators accept 0 examples\n") {
		t.Fatalf("check = %q, %v", out.String(), err)
	}
	// The renderer's and the check's arguments are no programs: a path that is not clean passes.
	withArgs := withOutput(t, base(), render, accepts, map[string]string{"hello.yaml": hello})
	for _, script := range []string{"/render\"", "/check\""} {
		withArgs["outputs.json"] = strings.Replace(withArgs["outputs.json"], script, strings.TrimSuffix(script, `"`)+` --dir /var/lib/ --at /run//x"`, 1)
	}
	if err := check(image(t, withArgs), &bytes.Buffer{}); err != nil {
		t.Fatalf("check with arguments = %v", err)
	}
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"an example its check refuses":  {withOutput(t, base(), render, accepts, map[string]string{"hello.yaml": hello, "other.yaml": strings.Replace(hello, "hello", "other", 1)}), "output greeting: example other.yaml: its check refused the rendered files (exit 1)"},
		"an example its renderer fails": {withOutput(t, base(), "exit 4", accepts, map[string]string{"hello.yaml": hello}), "output greeting: example hello.yaml: its renderer failed (exit 4)"},
		"an invalid example":            {withOutput(t, base(), render, accepts, map[string]string{"bad.yaml": strings.Replace(hello, "kind: Greeting", "kind: Greting", 1)}), "output greeting: example bad.yaml: "},
		"no examples":                   {withOutput(t, base(), render, accepts, map[string]string{"README": "none"}), "output greeting: its examples: "},
		"an undeclared kind": {func() map[string]string {
			f := withOutput(t, base(), render, accepts, nil)
			f["outputs.json"] = strings.Replace(f["outputs.json"], `["Greeting"]`, `["Peer"]`, 1)
			delete(f, "examples.json")
			return f
		}(), "output greeting: the image declares no kind Peer"},
		"a renderer outside the store": {func() map[string]string {
			f := withOutput(t, base(), render, accepts, nil)
			f["outputs.json"] = strings.Replace(f["outputs.json"], storeShell(t), "/bin/sh", 1)
			delete(f, "examples.json")
			return f
		}(), `output greeting: the renderer "/bin/sh `},
		"a renderer whose program is not clean": {func() map[string]string {
			f := withOutput(t, base(), render, accepts, nil)
			f["outputs.json"] = strings.Replace(f["outputs.json"], "/nix/store/", "/nix/store//", 1)
			delete(f, "examples.json")
			return f
		}(), `output greeting: the renderer "/nix/store//`},
		"an effect on no output": {func() map[string]string {
			f := withOutput(t, base(), render, accepts, nil)
			f["effects.json"] = `{"greeter": {"outputs": ["greting"], "restart": ["greeter.service"]}}`
			delete(f, "examples.json")
			return f
		}(), "effect greeter: outputs names greting, which is no output of the image; its outputs are greeting"},
		"an effect on an undeclared kind": {func() map[string]string {
			f := withOutput(t, base(), render, accepts, nil)
			f["effects.json"] = `{"greeter": {"kinds": ["Peer"], "restart": ["greeter.service"]}}`
			delete(f, "examples.json")
			return f
		}(), "effect greeter: the image declares no kind Peer"},
		"an effect on Secret documents": {func() map[string]string {
			f := withOutput(t, base(), render, accepts, nil)
			f["effects.json"] = `{"greeter": {"kinds": ["Secret"], "restart": ["greeter.service"]}}`
			delete(f, "examples.json")
			return f
		}(), "effect greeter: an effect does not fire on Secret documents"},
		"an effect on a withheld kind": {func() map[string]string {
			f := withBundle(withOutput(t, base(), render, accepts, nil))
			f["effects.json"] = `{"greeter": {"kinds": ["Bundle"], "restart": ["greeter.service"]}}`
			delete(f, "examples.json")
			return f
		}(), "effect greeter: an effect does not fire on Bundle documents"},
		"an output of a withheld kind": {func() map[string]string {
			f := withBundle(withOutput(t, base(), render, accepts, nil))
			f["outputs.json"] = strings.Replace(f["outputs.json"], `["Greeting"]`, `["Bundle"]`, 1)
			delete(f, "examples.json")
			return f
		}(), "output greeting: the image withholds Bundle documents from its programs"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(image(t, tc.files), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// withBundle adds to files the schema of Bundle, a kind the image withholds from its services.
func withBundle(files map[string]string) map[string]string {
	files["schemas/bundle.example.org_v1alpha1_Bundle.json"] = `{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "x-chalkos-kind": {"group": "bundle.example.org", "version": "v1alpha1", "kind": "Bundle", "multiple": true, "withheld": true}}`
	return files
}

// withValidator adds to files the validator greeting of the Greeting documents, whose program
// runs the shell commands given, with the examples given.
func withValidator(t *testing.T, files map[string]string, commands string, examples map[string]string) map[string]string {
	t.Helper()
	sh := storeShell(t)
	scripts := t.TempDir()
	os.WriteFile(filepath.Join(scripts, "validate"), []byte("set -e\n"+commands+"\n"), 0o644)
	files["validators.json"] = `{"greeting": {"inputs": ["Greeting"], "program": "` + sh + " " + filepath.Join(scripts, "validate") + `"}}`
	files["examples.json"] = `{"validators": {"greeting": "` + filepath.Join(scripts, "examples") + `"}}`
	for name, text := range examples {
		path := filepath.Join(scripts, "examples", name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(text), 0o644)
	}
	return files
}

// The image's build runs each validator on each of its examples, which it must accept, and
// refuses a validator it could not run on a node.
func TestCheckValidators(t *testing.T) {
	// The validator refuses a Greeting whose message is other.
	validate := `if grep -q '"message":"other"'; then echo '{"refusals": [{"kind": "Greeting", "name": "g", "path": "spec.message", "message": "say hello"}]}'; fi`
	base := func() map[string]string { return files(greeting(fieldRule, kindRule), lock, "{}") }
	var out bytes.Buffer
	if err := check(image(t, withValidator(t, base(), validate, map[string]string{"hello.yaml": hello})), &out); err != nil || !strings.HasSuffix(out.String(), "; 0 outputs render 0 examples; 1 validators accept 1 examples\n") {
		t.Fatalf("check = %q, %v", out.String(), err)
	}
	other := strings.Replace(hello, "hello", "other", 1)
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"an example it refuses": {withValidator(t, base(), validate, map[string]string{"hello.yaml": hello, "other.yaml": other}), "validator greeting: example other.yaml: the config is refused: Greeting/g: spec.message: say hello"},
		"an example it fails":   {withValidator(t, base(), "exit 4", map[string]string{"hello.yaml": hello}), "validator greeting: example hello.yaml: its program failed (exit 4)"},
		"an invalid example":    {withValidator(t, base(), validate, map[string]string{"bad.yaml": strings.Replace(hello, "kind: Greeting", "kind: Greting", 1)}), "validator greeting: example bad.yaml: "},
		"no examples":           {withValidator(t, base(), validate, map[string]string{"README": "none"}), "validator greeting: its examples: "},
		"examples of no validator": {func() map[string]string {
			f := withValidator(t, base(), validate, nil)
			f["examples.json"] = `{"validators": {"greting": "/nowhere"}}`
			return f
		}(), "validator greting: it has examples but is no validator"},
		"an undeclared kind": {func() map[string]string {
			f := withValidator(t, base(), validate, nil)
			f["validators.json"] = strings.Replace(f["validators.json"], `["Greeting"]`, `["Peer"]`, 1)
			delete(f, "examples.json")
			return f
		}(), "validator greeting: the image declares no kind Peer"},
		"a withheld kind": {func() map[string]string {
			f := withBundle(withValidator(t, base(), validate, nil))
			f["validators.json"] = strings.Replace(f["validators.json"], `["Greeting"]`, `["Bundle"]`, 1)
			delete(f, "examples.json")
			return f
		}(), "validator greeting: the image withholds Bundle documents from its programs"},
		"a program outside the store": {func() map[string]string {
			f := withValidator(t, base(), validate, nil)
			f["validators.json"] = strings.Replace(f["validators.json"], storeShell(t), "/bin/sh", 1)
			delete(f, "examples.json")
			return f
		}(), `validator greeting: the program "/bin/sh `},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(image(t, tc.files), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
