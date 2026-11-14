package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testImage lays out an image with the built-in kinds, as every image has them, and the Greeting
// kind, whose field rule refuses a change of the message while facts.lock is set; the output
// greeting, which writes the message to greeting.txt and whose check refuses the message boom;
// the validator greeting, which refuses the message other; the effect greeter on the output and
// the effect greeter-reload, due when facts.share changes.
func testImage(t *testing.T) string {
	t.Helper()
	render := `echo "$(sed -n 's/.*"message":"\([^"]*\)".*/\1/p')" > "$OUT/greeting.txt"`
	refuseBoom := `! grep -qx boom "$OUT/greeting.txt"`
	refuseOther := `if grep -q '"message":"other"'; then echo '{"refusals": [{"kind": "Greeting", "name": "g", "path": "spec.message", "message": "say hello"}]}'; fi`
	f := withValidator(t, withOutput(t, files(greeting(fieldRule, kindRule), lock, "{}"), render, refuseBoom), refuseOther)
	f["effects.json"] = `{"greeter": {"outputs": ["greeting"], "restart": ["greeter.service"]},
	  "greeter-reload": {"when": "facts.share != oldFacts.share", "reload": ["greeter.service"]}}`
	// A test's config needs none of the built-in kinds, the SystemDisk among them.
	builtin := "../../test/fixtures/kinds/builtin"
	entries, err := os.ReadDir(builtin)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(builtin, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		f["schemas/"+e.Name()] = string(data)
	}
	return image(t, f)
}

// say is a Greeting whose message is the one given.
func say(message string) string {
	return strings.Replace(hello, "message: hello", "message: "+message, 1)
}

// writeFiles writes each file of files, by its path below dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRun(t *testing.T) {
	img := testImage(t)
	configs := map[string]string{
		"hello.yaml": hello,
		"hi.yaml":    say("hi"),
		"other.yaml": say("other"),
		"boom.yaml":  say("boom"),
		"store.yaml": say("/nix/store/0123456789abcdfghijklmnpqrsvwxyz-x/bin/y and /nix/store/short-x"),
		"typo.yaml":  strings.Replace(hello, "kind: Greeting", "kind: Greting", 1),
	}
	const greeterRefusal = `{"kind":"Greeting","name":"g","path":"spec.message","message":"say hello"}`
	// $C is the directory of the configs, $G that of the golden files.
	cases := map[string]struct {
		tests       string
		goldens     map[string]string
		wantFiles   map[string]string
		wantMissing []string
	}{
		"a golden that matches": {
			tests:   `{"t": {"config": "$C/hello.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}}}`,
			goldens: map[string]string{"greeting/greeting.txt": "hello\n"},
			wantFiles: map[string]string{
				"summary.txt":                     "PASS t\n",
				"failures":                        "",
				"goldens.tsv":                     "",
				"t/outputs/greeting/greeting.txt": "hello\n",
				"t/refusals.json":                 "[]\n",
				"t/effects.json":                  "[]\n",
			},
		},
		"a config of extension documents alone": {
			tests:   `{"t": {"config": "$C/hello.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}, "refusals": []}}`,
			goldens: map[string]string{"greeting/greeting.txt": "hello\n"},
			wantFiles: map[string]string{
				"summary.txt":     "PASS t\n",
				"t/refusals.json": "[]\n",
			},
		},
		"a golden that differs": {
			tests:   `{"t": {"config": "$C/hello.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}}}`,
			goldens: map[string]string{"greeting/greeting.txt": "hi\n"},
			wantFiles: map[string]string{
				"summary.txt":                     "FAIL t: output greeting differs from its golden files\n",
				"failures":                        "FAIL t: output greeting differs from its golden files\n",
				"goldens.tsv":                     "t\tgreeting\t$G/greeting\tt/outputs/greeting\n",
				"t/outputs/greeting/greeting.txt": "hello\n",
			},
		},
		"a golden file missing from the render": {
			tests:   `{"t": {"config": "$C/hello.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}}}`,
			goldens: map[string]string{"greeting/greeting.txt": "hello\n", "greeting/extra/motd": "welcome\n"},
			wantFiles: map[string]string{
				"failures":    "FAIL t: output greeting differs from its golden files\n",
				"goldens.tsv": "t\tgreeting\t$G/greeting\tt/outputs/greeting\n",
			},
			wantMissing: []string{"t/outputs/greeting/extra"},
		},
		"an output the image does not declare": {
			tests: `{"t": {"config": "$C/hello.yaml", "node": "example", "outputs": {"greeter": "$G/greeter"}}}`,
			wantFiles: map[string]string{
				"failures": "FAIL t: output greeter rendered nothing; the image declares no output greeter\n",
			},
		},
		"an accepted config, refusals expected": {
			tests: `{"t": {"config": "$C/hello.yaml", "node": "example", "refusals": [` + greeterRefusal + `]}}`,
			wantFiles: map[string]string{
				"failures":        "FAIL t: refusals differ: expected [" + greeterRefusal + "], got []\n",
				"t/refusals.json": "[]\n",
			},
		},
		"a refusal of the validator": {
			tests: `{"t": {"config": "$C/other.yaml", "node": "example", "refusals": [` + greeterRefusal + `]}}`,
			wantFiles: map[string]string{
				"summary.txt":     "PASS t\n",
				"failures":        "",
				"t/refusals.json": "[" + greeterRefusal + "]\n",
				"t/effects.json":  "[]\n",
			},
			wantMissing: []string{"t/outputs"},
		},
		"an output of a refused config": {
			tests:   `{"t": {"config": "$C/other.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}, "refusals": [` + greeterRefusal + `]}}`,
			goldens: map[string]string{"greeting/greeting.txt": "other\n"},
			wantFiles: map[string]string{
				"failures":        "FAIL t: output greeting rendered nothing; the config was refused\n",
				"goldens.tsv":     "",
				"t/refusals.json": "[" + greeterRefusal + "]\n",
			},
			wantMissing: []string{"t/outputs"},
		},
		"a refusal of the output's check": {
			tests: `{"t": {"config": "$C/boom.yaml", "node": "example", "refusals": [{"kind": "", "name": "", "path": "", "message": "output greeting: its check refused the rendered files (exit 1)"}]}}`,
			wantFiles: map[string]string{
				"summary.txt":     "PASS t\n",
				"t/refusals.json": `[{"kind":"","name":"","path":"","message":"output greeting: its check refused the rendered files (exit 1)"}]` + "\n",
			},
		},
		"a refusal of a change rule": {
			tests: `{"t": {"config": "$C/hi.yaml", "before": "$C/hello.yaml", "node": "example", "facts": {"lock": true}, "effects": [],
			  "refusals": [{"kind": "Greeting", "name": "g", "path": "spec.message", "message": "locked"}]}}`,
			wantFiles: map[string]string{
				"summary.txt":     "PASS t\n",
				"t/refusals.json": `[{"kind":"Greeting","name":"g","path":"spec.message","message":"locked"}]` + "\n",
				"t/effects.json":  "[]\n",
			},
		},
		"a schema refusal": {
			tests: `{"t": {"config": "$C/typo.yaml", "node": "example", "refusals": [{"kind": "", "name": "", "path": "", "message": "does not accept kind \"Greting\" of apiVersion \"greeting.example.org/v1alpha1\""}]}}`,
			wantFiles: map[string]string{
				"summary.txt":     "PASS t\n",
				"t/refusals.json": `[{"kind":"","name":"","path":"","message":"does not accept kind \"Greting\" of apiVersion \"greeting.example.org/v1alpha1\""}]` + "\n",
			},
		},
		"effects of a change": {
			tests: `{"t": {"config": "$C/hi.yaml", "before": "$C/hello.yaml", "node": "example", "effects": ["greeter"], "refusals": []}}`,
			wantFiles: map[string]string{
				"summary.txt":                     "PASS t\n",
				"t/effects.json":                  `["greeter"]` + "\n",
				"t/refusals.json":                 "[]\n",
				"t/outputs/greeting/greeting.txt": "hi\n",
			},
		},
		"effects of a rule on the facts": {
			tests: `{"t": {"config": "$C/hello.yaml", "before": "$C/hello.yaml", "node": "example", "facts": {"share": "b"}, "oldFacts": {"share": "a"}, "effects": ["greeter-reload"]}}`,
			wantFiles: map[string]string{
				"summary.txt":    "PASS t\n",
				"t/effects.json": `["greeter-reload"]` + "\n",
			},
		},
		"effects that differ": {
			tests: `{"t": {"config": "$C/hi.yaml", "before": "$C/hello.yaml", "node": "example", "effects": []}}`,
			wantFiles: map[string]string{
				"failures":       "FAIL t: effects differ: expected [], got [\"greeter\"]\n",
				"t/effects.json": `["greeter"]` + "\n",
			},
		},
		"a refused before": {
			tests: `{"t": {"config": "$C/hello.yaml", "before": "$C/other.yaml", "node": "example", "effects": []}}`,
			wantFiles: map[string]string{
				"failures": "FAIL t: before is refused: the config is refused: Greeting/g: spec.message: say hello\n",
			},
			wantMissing: []string{"t/refusals.json", "t/effects.json"},
		},
		"a config that cannot be read": {
			tests: `{"t": {"config": "$C/missing.yaml", "node": "example"}}`,
			wantFiles: map[string]string{
				"failures": "FAIL t: the check failed: open $C/missing.yaml: no such file or directory\n",
			},
		},
		"store paths normalised": {
			tests:   `{"t": {"config": "$C/store.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}}}`,
			goldens: map[string]string{"greeting/greeting.txt": "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-x/bin/y and /nix/store/short-x\n"},
			wantFiles: map[string]string{
				"summary.txt":                     "PASS t\n",
				"t/outputs/greeting/greeting.txt": "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-x/bin/y and /nix/store/short-x\n",
			},
		},
		"several tests and problems": {
			tests: `{"b": {"config": "$C/hi.yaml", "before": "$C/hello.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}, "refusals": [` + greeterRefusal + `], "effects": []},
			  "a": {"config": "$C/hello.yaml", "node": "example"}}`,
			goldens: map[string]string{"greeting/greeting.txt": "hello\n"},
			wantFiles: map[string]string{
				"summary.txt": "PASS a\n" +
					"FAIL b: output greeting differs from its golden files\n" +
					"FAIL b: refusals differ: expected [" + greeterRefusal + "], got []\n" +
					"FAIL b: effects differ: expected [], got [\"greeter\"]\n",
				"failures": "FAIL b: output greeting differs from its golden files\n" +
					"FAIL b: refusals differ: expected [" + greeterRefusal + "], got []\n" +
					"FAIL b: effects differ: expected [], got [\"greeter\"]\n",
				"goldens.tsv": "b\tgreeting\t$G/greeting\tb/outputs/greeting\n",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c, g, out := filepath.Join(dir, "configs"), filepath.Join(dir, "golden"), filepath.Join(dir, "out")
			expand := strings.NewReplacer("$C", c, "$G", g).Replace
			writeFiles(t, c, configs)
			writeFiles(t, g, tc.goldens)
			writeFiles(t, dir, map[string]string{"tests.json": `{"platform": "metal", "tests": ` + expand(tc.tests) + `}`})
			if err := runTests(img, filepath.Join(dir, "tests.json"), out); err != nil {
				t.Fatalf("runTests = %v", err)
			}
			for path, want := range tc.wantFiles {
				got, err := os.ReadFile(filepath.Join(out, path))
				if err != nil || string(got) != expand(want) {
					t.Errorf("%s = %q, %v; want %q", path, got, err, expand(want))
				}
			}
			for _, path := range tc.wantMissing {
				if _, err := os.Stat(filepath.Join(out, path)); !os.IsNotExist(err) {
					t.Errorf("%s exists, want none: %v", path, err)
				}
			}
		})
	}
}

// The runner fails only when it cannot run the tests at all.
func TestRunCannotRun(t *testing.T) {
	img := testImage(t)
	for name, tc := range map[string]struct{ tests, want string }{
		"tests that are no JSON":      {`{"tests": `, "tests.json: unexpected EOF"},
		"an unknown field":            {`{"platform": "metal", "tests": {"t": {"config": "x", "output": {}}}}`, `unknown field "output"`},
		"a test that is no file name": {`{"platform": "metal", "tests": {"a/b": {"config": "x"}}}`, `the test "a/b" is no file name`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"tests.json": tc.tests})
			if err := runTests(img, filepath.Join(dir, "tests.json"), filepath.Join(dir, "out")); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if err := runTests(img, filepath.Join(t.TempDir(), "missing.json"), t.TempDir()); err == nil {
		t.Error("runTests of a missing file of tests succeeded")
	}
}
