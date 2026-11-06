package validators

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/schema"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -e\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const demos = `apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: two}
spec: {name: second, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

func input(t *testing.T) outputs.Input {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(demos))
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/image/schemas")
	if err != nil {
		t.Fatal(err)
	}
	return outputs.Input{Node: "cp1", Documents: docs, Schemas: s}
}

func runner(t *testing.T) outputs.Renderer {
	return outputs.Renderer{Root: t.TempDir(), Run: outputs.Direct{}}
}

// refuse is a shell command that prints a report refusing the Demo of the name at spec.name.
func refuse(name, message string) string {
	return `echo '{"refusals": [{"kind": "Demo", "name": "` + name + `", "path": "spec.name", "message": "` + message + `"}]}'`
}

// A validator reads the documents of its kinds as one JSON array, sorted by name, without the
// Secrets and with secret values hashed, and runs as "<program> validate <name>" with an empty
// $OUT; one that prints no refusal passes, and one that prints refusals refuses the config with
// them, each once across validators.
func TestRun(t *testing.T) {
	r := runner(t)
	seen := filepath.Join(t.TempDir(), "seen")
	defs := map[string]Definition{
		"passes": {Inputs: []string{"Demo"}, Program: script(t, `cat >`+seen+`.json; echo "$*" >`+seen+`.args; ls -A "$OUT" >`+seen+`.out`)},
		"quiet":  {Inputs: []string{"Demo"}, Program: script(t, `echo '{"refusals": []}'`)},
		"one":    {Inputs: []string{"Demo"}, Program: script(t, refuse("one", "is taken"))},
		"again":  {Inputs: []string{"Demo"}, Program: script(t, refuse("one", "is taken")+`; echo detail >&2`)},
	}
	rs, failures, err := Run(context.Background(), r, defs, input(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) > 0 {
		t.Fatal(failures)
	}
	want := []refusals.Refusal{{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}}
	if !slices.Equal(rs, append(want, want...)) {
		t.Errorf("refusals = %v", rs)
	}
	if got := Refusal(rs, nil); got != "the config is refused: Demo/one: spec.name: is taken" {
		t.Errorf("Refusal = %q", got)
	}
	data, err := os.ReadFile(seen + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	if err := json.Unmarshal(data, &docs); err != nil {
		t.Fatalf("stdin %s: %v", data, err)
	}
	if len(docs) != 2 || docs[0]["metadata"].(map[string]any)["name"] != "one" || strings.Contains(string(data), "s3cret") || strings.Contains(string(data), `"Secret"`) {
		t.Errorf("stdin = %s", data)
	}
	if args, _ := os.ReadFile(seen + ".args"); string(args) != "validate passes\n" {
		t.Errorf("arguments = %q", args)
	}
	if out, _ := os.ReadFile(seen + ".out"); len(out) != 0 {
		t.Errorf("$OUT held %q", out)
	}
	if _, err := os.Stat(filepath.Join(r.Root, ".validating")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the validators' directory stays: %v", err)
	}
}

// A validator that fails, prints no report or does not finish fails the config, whatever it
// printed: its failure names it and how it failed, never what it wrote to standard error.
func TestRunFailures(t *testing.T) {
	r := runner(t)
	r.Timeout = time.Second
	defs := map[string]Definition{
		"exits":     {Inputs: []string{"Demo"}, Program: script(t, refuse("one", "is taken")+`; echo detail >&2; exit 3`)},
		"rambles":   {Inputs: []string{"Demo"}, Program: script(t, `echo checked`)},
		"malformed": {Inputs: []string{"Demo"}, Program: script(t, refuse("One", "is taken"))},
		"sleeps":    {Inputs: []string{"Demo"}, Program: script(t, `exec sleep 10`)},
		"passes":    {Inputs: []string{"Demo"}, Program: script(t, `true`)},
	}
	rs, failures, err := Run(context.Background(), r, defs, input(t))
	if err != nil {
		t.Fatal(err)
	}
	if rs != nil {
		t.Errorf("refusals = %v", rs)
	}
	want := []Failure{
		{"exits", "failed (exit 3); see the node's journal"},
		{"malformed", `failed: its standard output: refusal 1: name "One" is no document name`},
		{"rambles", "failed: its standard output: it is no report of refusals"},
		{"sleeps", "did not finish within 1s"},
	}
	if !slices.Equal(failures, want) {
		t.Errorf("failures = %#v", failures)
	}
	if got := Refusal([]refusals.Refusal{{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}}, failures[:1]); got != "the config is refused: Demo/one: spec.name: is taken; validator exits failed (exit 3); see the node's journal" {
		t.Errorf("Refusal = %q", got)
	}
}

// recorder is a runner that records the programs it is given and runs none.
type recorder struct{ programs []outputs.Program }

func (r *recorder) Run(_ context.Context, p outputs.Program) ([]byte, error) {
	r.programs = append(r.programs, p)
	return nil, nil
}

// A validator runs as a renderer runs, sandboxed on a node, but may neither write its $OUT nor
// open netlink sockets as a check may, and reports refusals on standard output.
func TestRunProgram(t *testing.T) {
	rec := &recorder{}
	r := outputs.Renderer{Root: t.TempDir(), Run: rec}
	defs := map[string]Definition{"peers": {Inputs: []string{"Demo"}, Program: "/nix/store/x/bin/v --strict"}}
	if _, failures, err := Run(context.Background(), r, defs, input(t)); err != nil || failures != nil {
		t.Fatal(err, failures)
	}
	if len(rec.programs) != 1 {
		t.Fatalf("programs = %v", rec.programs)
	}
	p := rec.programs[0]
	if !slices.Equal(p.Argv, []string{"/nix/store/x/bin/v", "--strict", "validate", "peers"}) || p.Write || p.Netlink || !p.Report ||
		p.Timeout != outputs.DefaultTimeout || filepath.Dir(p.Out) != filepath.Join(r.Root, ".validating") {
		t.Errorf("program = %+v", p)
	}
}

// Read takes the validators an image declares and refuses one chalkd could not run.
func TestRead(t *testing.T) {
	dir := t.TempDir()
	if defs, err := Read(filepath.Join(dir, "none.json")); err != nil || len(defs) != 0 {
		t.Fatalf("a missing file: %v, %v", defs, err)
	}
	for text, want := range map[string]string{
		`{"peers": {"inputs": ["Peer"], "program": "/nix/store/x/bin/v --strict"}}`: "",
		`{"Peers": {"inputs": ["Peer"], "program": "/nix/store/x/bin/v"}}`:          "a validator's name is lower-case letters, digits and dashes",
		`{"peers": {"inputs": ["Peer"], "program": "v"}}`:                           `the program "v" names no program by its absolute path`,
		`{"peers": {"inputs": [], "program": "/nix/store/x/bin/v"}}`:                "it reads no kind",
		`{"peers": {"inputs": ["Secret"], "program": "/nix/store/x/bin/v"}}`:        "a validator does not read Secret documents",
	} {
		path := filepath.Join(dir, "validators.json")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		defs, err := Read(path)
		switch {
		case want == "" && (err != nil || defs["peers"].Program != "/nix/store/x/bin/v --strict"):
			t.Errorf("%s: %v, %v", text, defs, err)
		case want != "" && (err == nil || !strings.HasSuffix(err.Error(), want)):
			t.Errorf("%s: %v, want an error ending in %q", text, err, want)
		}
	}
}

// A linked validator reads the JSON a validator's program reads, and its refusals are held to the
// format a program's report is: a refusal outside it, or an error, fails the validator. A message
// that quotes a rune refusals.Unprintable reports stays a refusal, with a space for the rune.
func TestRunLinked(t *testing.T) {
	var read []byte
	taken := refusals.Refusal{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}
	linked := []Linked{
		{Name: "reads", Inputs: []string{"Demo"}, Validate: func(docs []byte) ([]refusals.Refusal, error) {
			read = docs
			return []refusals.Refusal{taken, taken}, nil
		}},
		{Name: "errs", Inputs: []string{"Demo"}, Validate: func([]byte) ([]refusals.Refusal, error) {
			return nil, errors.New("the documents are no JSON array")
		}},
		{Name: "malformed", Inputs: []string{"Demo"}, Validate: func([]byte) ([]refusals.Refusal, error) {
			return []refusals.Refusal{{Kind: "Demo", Name: "one", Path: "spec name", Message: "is taken"}}, nil
		}},
		{Name: "quotes", Inputs: []string{"Demo"}, Validate: func([]byte) ([]refusals.Refusal, error) {
			return []refusals.Refusal{{Kind: "Demo", Name: "two", Path: "spec.name", Message: "two\nlines\u202e"}}, nil
		}},
	}
	in := input(t)
	rs, failures := RunLinked(linked, in)
	if !slices.Equal(rs, []refusals.Refusal{taken, {Kind: "Demo", Name: "two", Path: "spec.name", Message: "two lines "}}) {
		t.Errorf("refusals = %v", rs)
	}
	want := []Failure{
		{"errs", "failed: the documents are no JSON array"},
		{"malformed", `failed: its report: refusal 1: path "spec name" is no field's path`},
	}
	if !slices.Equal(failures, want) {
		t.Errorf("failures = %#v", failures)
	}
	stdin, err := Stdin(in, []string{"Demo"})
	if err != nil || string(read) != string(stdin) {
		t.Errorf("the linked validator read %s, a program %s: %v", read, stdin, err)
	}
}

// A directory chalkd cannot prepare for its validators is an error of chalkd's own, which fails
// no validator and refuses nothing.
func TestRunDirectoryError(t *testing.T) {
	rec := &recorder{}
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := outputs.Renderer{Root: root, Run: rec}
	defs := map[string]Definition{"peers": {Inputs: []string{"Demo"}, Program: "/nix/store/x/bin/v"}}
	rs, failures, err := Run(context.Background(), r, defs, input(t))
	if err == nil || rs != nil || failures != nil || len(rec.programs) != 0 {
		t.Errorf("Run = %v, %v, %v; programs %v", rs, failures, err, rec.programs)
	}
}
