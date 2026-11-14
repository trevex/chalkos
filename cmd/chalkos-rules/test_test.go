package main

import (
	"fmt"
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
		"store.yaml": say("/nix/store/0123456789abcdfghijklmnpqrsvwxyz-x and /nix/store/short-x and /nix/store/0123456789abcdefghijklmnopqrstuv-z"),
		"typo.yaml":  strings.Replace(hello, "kind: Greeting", "kind: Greting", 1),
	}
	const greeterRefusal = `{"kind":"Greeting","name":"g","path":"spec.message","message":"say hello"}`
	// $C is the directory of the configs, $G that of the golden files.
	cases := map[string]struct {
		tests   string
		goldens map[string]string
		// links are the symbolic links below the golden files' directory, by relative path, to
		// their targets.
		links       map[string]string
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
			wantMissing: []string{"t/warnings"},
		},
		"a config of extension documents alone, refusals: []": {
			tests: `{"t": {"config": "$C/hello.yaml", "node": "example", "refusals": []}}`,
			wantFiles: map[string]string{
				"summary.txt":     "PASS t\n",
				"t/refusals.json": "[]\n",
			},
		},
		"a refused config, no refusals expected": {
			tests: `{"t": {"config": "$C/other.yaml", "node": "example"}}`,
			wantFiles: map[string]string{
				"failures":        "FAIL t: refusals differ: expected [], got [" + greeterRefusal + "]\n",
				"t/refusals.json": "[" + greeterRefusal + "]\n",
			},
		},
		"a golden that holds a link": {
			tests:   `{"t": {"config": "$C/hello.yaml", "node": "example", "outputs": {"greeting": "$G/greeting"}}}`,
			goldens: map[string]string{"greeting/greeting.txt": "hello\n", "greeting/real": "x"},
			links:   map[string]string{"greeting/link": "real"},
			wantFiles: map[string]string{
				"failures":    "FAIL t: output greeting: its golden files hold link, which is no regular file\n",
				"goldens.tsv": "",
			},
		},
		"facts the image does not declare": {
			tests: `{"t": {"config": "$C/hello.yaml", "node": "example", "facts": {"locked": true, "lock": null}, "oldFacts": {"shared": 1}}}`,
			wantFiles: map[string]string{
				"failures": "FAIL t: facts names locked, which the image does not declare; it declares lock, share\n" +
					"FAIL t: oldFacts names shared, which the image does not declare; it declares lock, share\n",
				"t/refusals.json": "[]\n",
			},
		},
		"a before that cannot be read": {
			tests: `{"t": {"config": "$C/hello.yaml", "before": "$C/missing.yaml", "node": "example"}}`,
			wantFiles: map[string]string{
				"failures": "FAIL t: the check of before failed: open $C/missing.yaml: no such file or directory\n",
			},
			wantMissing: []string{"t"},
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
			goldens: map[string]string{"greeting/greeting.txt": "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-x and /nix/store/short-x and /nix/store/0123456789abcdefghijklmnopqrstuv-z\n"},
			wantFiles: map[string]string{
				"summary.txt":                     "PASS t\n",
				"t/outputs/greeting/greeting.txt": "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-x and /nix/store/short-x and /nix/store/0123456789abcdefghijklmnopqrstuv-z\n",
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
			for path, target := range tc.links {
				if err := os.Symlink(target, filepath.Join(g, path)); err != nil {
					t.Fatal(err)
				}
			}
			writeFiles(t, dir, map[string]string{"tests.json": `{"tests": ` + expand(tc.tests) + `}`})
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
		"tests that are no JSON":                {`{"tests": `, "tests.json: unexpected EOF"},
		"an unknown field":                      {`{"tests": {"t": {"config": "x", "output": {}}}}`, `unknown field "output"`},
		"a test that is no file name":           {`{"tests": {"a/b": {"config": "x"}}}`, `the test "a/b" is no name of a directory of its own`},
		"a test named as the summary":           {`{"tests": {"summary.txt": {"config": "x"}}}`, `the test "summary.txt" is no name`},
		"a test named as the failures":          {`{"tests": {"failures": {"config": "x"}}}`, `the test "failures" is no name`},
		"a test named as the goldens":           {`{"tests": {"goldens.tsv": {"config": "x"}}}`, `the test "goldens.tsv" is no name`},
		"a test named with a control character": {`{"tests": {"a\tb": {"config": "x"}}}`, `the test "a\tb" is no name`},
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

// An error of the check that is no refusal is the check's failure, which refusalsOf returns, and
// which tester.run reports as "the check failed: …".
func TestRefusalsOfAFailure(t *testing.T) {
	failure := fmt.Errorf("the image's validators: %w", os.ErrPermission)
	if got, err := refusalsOf(failure); got != nil || err != failure {
		t.Errorf("refusalsOf = %v, %v; want the error itself", got, err)
	}
	if got, err := refusalsOf(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("refusalsOf(nil) = %#v, %v; want none", got, err)
	}
}

// An effect rule that cannot be evaluated warns, in the test's warnings and the summary, and takes
// its effect all the same, as chalkd does; the warning fails no test.
func TestRunWarnings(t *testing.T) {
	img := testImage(t)
	writeFiles(t, img, map[string]string{
		"facts.json":   `{"count": {"file": "/run/count.json"}}`,
		"effects.json": `{"greeter": {"when": "facts.count > 1", "reload": ["greeter.service"]}}`,
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	writeFiles(t, dir, map[string]string{
		"hello.yaml": hello,
		"tests.json": `{"tests": {"t": {"config": "` + filepath.Join(dir, "hello.yaml") + `", "before": "` + filepath.Join(dir, "hello.yaml") + `",
		  "node": "example", "facts": {"count": "many"}, "effects": ["greeter"]}}}`,
	})
	if err := runTests(img, filepath.Join(dir, "tests.json"), out); err != nil {
		t.Fatal(err)
	}
	const warning = "the rule of effect greeter: a rule cannot be evaluated: no such overload; taking the effect all the same"
	for path, want := range map[string]string{
		"summary.txt": "PASS t\nWARN t: " + warning + "\n",
		"failures":    "",
		"t/warnings":  warning + "\n",
	} {
		if got, err := os.ReadFile(filepath.Join(out, path)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
}
