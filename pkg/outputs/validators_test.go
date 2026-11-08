package outputs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/refusals"
)

// refuse is a shell command that prints a report refusing the Demo of the name at spec.name.
func refuse(name, message string) string {
	return `echo '{"refusals": [{"kind": "Demo", "name": "` + name + `", "path": "spec.name", "message": "` + message + `"}]}'`
}

// A validator reads what a renderer of its kinds reads and runs as "<program> validate <name>"
// with an empty $OUT; one that prints no refusal passes, and one that prints refusals refuses the
// config with them, which the message reports once across validators.
func TestValidate(t *testing.T) {
	r := renderer(t)
	seen := filepath.Join(t.TempDir(), "seen")
	defs := map[string]Validator{
		"passes": {Inputs: []string{"Demo"}, Program: script(t, `cat >`+seen+`.json; echo "$*" >`+seen+`.args; ls -A "$OUT" >`+seen+`.out`)},
		"quiet":  {Inputs: []string{"Demo"}, Program: script(t, `echo '{"refusals": []}'`)},
		"one":    {Inputs: []string{"Demo"}, Program: script(t, refuse("one", "is taken"))},
		"again":  {Inputs: []string{"Demo"}, Program: script(t, refuse("one", "is taken")+`; echo detail >&2`)},
	}
	in := input(t, demos)
	failures, err := r.Validate(context.Background(), defs, in)
	if err != nil {
		t.Fatal(err)
	}
	taken := []refusals.Refusal{{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}}
	want := []Failure{
		{Name: "again", Role: RoleValidator, Problem: "its program refused the config: Demo/one: spec.name: is taken", Refusals: taken},
		{Name: "one", Role: RoleValidator, Problem: "its program refused the config: Demo/one: spec.name: is taken", Refusals: taken},
	}
	if !slices.EqualFunc(failures, want, sameFailure) {
		t.Errorf("failures = %#v", failures)
	}
	if got := Message(failures); got != "the config is refused: Demo/one: spec.name: is taken" {
		t.Errorf("Message = %q", got)
	}
	data, err := os.ReadFile(seen + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if stdin, err := in.stdin([]string{"Demo"}); err != nil || string(data) != string(stdin) {
		t.Errorf("a validator read %s, a renderer %s: %v", data, stdin, err)
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

func sameFailure(a, b Failure) bool {
	return a.Name == b.Name && a.Role == b.Role && a.Problem == b.Problem && slices.Equal(a.Refusals, b.Refusals)
}

// A validator that fails, prints no report or does not finish fails the config: its failure
// names it and how it failed, never what it wrote to standard error. A report counts whatever the
// exit status, as a renderer's does.
func TestValidateFailures(t *testing.T) {
	r := renderer(t)
	r.Timeout = time.Second
	defs := map[string]Validator{
		"exits":     {Inputs: []string{"Demo"}, Program: script(t, `echo detail >&2; exit 3`)},
		"refuses":   {Inputs: []string{"Demo"}, Program: script(t, refuse("one", "is taken")+`; exit 1`)},
		"rambles":   {Inputs: []string{"Demo"}, Program: script(t, `echo checked`)},
		"malformed": {Inputs: []string{"Demo"}, Program: script(t, refuse("One", "is taken"))},
		"sleeps":    {Inputs: []string{"Demo"}, Program: script(t, `exec sleep 10`)},
		"passes":    {Inputs: []string{"Demo"}, Program: script(t, `true`)},
	}
	failures, err := r.Validate(context.Background(), defs, input(t, demos))
	if err != nil {
		t.Fatal(err)
	}
	taken := []refusals.Refusal{{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}}
	want := []Failure{
		{Name: "exits", Role: RoleValidator, Problem: "its program failed (exit 3); see the node's journal"},
		{Name: "malformed", Role: RoleValidator, Problem: `its program's standard output: refusal 1: name "One" is no document name`},
		{Name: "rambles", Role: RoleValidator, Problem: "its program's standard output: it is no report of refusals"},
		{Name: "refuses", Role: RoleValidator, Problem: "its program refused the config: Demo/one: spec.name: is taken", Refusals: taken},
		{Name: "sleeps", Role: RoleValidator, Problem: "its program did not finish within 1s"},
	}
	if !slices.EqualFunc(failures, want, sameFailure) {
		t.Errorf("failures = %#v", failures)
	}
	if got := Message(failures[:2]); got != "validator exits: its program failed (exit 3); see the node's journal; validator malformed: its program's standard output: refusal 1: name \"One\" is no document name" {
		t.Errorf("Message = %q", got)
	}
	if got := Message(failures[2:4]); got != "the config is refused: Demo/one: spec.name: is taken; validator rambles: its program's standard output: it is no report of refusals" {
		t.Errorf("Message = %q", got)
	}
}

// recorder is a runner that records the programs it is given and runs none.
type recorder struct{ programs []Program }

func (r *recorder) Run(_ context.Context, p Program) ([]byte, error) {
	r.programs = append(r.programs, p)
	return nil, nil
}

// A validator runs as a renderer runs, sandboxed on a node, but may neither write its $OUT nor
// open netlink sockets as a check may, and reports refusals on standard output.
func TestValidateProgram(t *testing.T) {
	rec := &recorder{}
	r := Renderer{Root: t.TempDir(), Run: rec}
	defs := map[string]Validator{"peers": {Inputs: []string{"Demo"}, Program: "/nix/store/x/bin/v --strict"}}
	if failures, err := r.Validate(context.Background(), defs, input(t, demos)); err != nil || failures != nil {
		t.Fatal(err, failures)
	}
	if len(rec.programs) != 1 {
		t.Fatalf("programs = %v", rec.programs)
	}
	p := rec.programs[0]
	if !slices.Equal(p.Argv, []string{"/nix/store/x/bin/v", "--strict", "validate", "peers"}) || p.Write || p.Netlink || !p.Report ||
		p.Timeout != DefaultTimeout || filepath.Dir(p.Out) != filepath.Join(r.Root, ".validating") {
		t.Errorf("program = %+v", p)
	}
}

// ReadValidators takes the validators an image declares and refuses one chalkd could not run.
func TestReadValidators(t *testing.T) {
	dir := t.TempDir()
	if defs, err := ReadValidators(filepath.Join(dir, "none.json")); err != nil || len(defs) != 0 {
		t.Fatalf("a missing file: %v, %v", defs, err)
	}
	for text, want := range map[string]string{
		`{"peers": {"inputs": ["Peer"], "program": "/nix/store/x/bin/v --strict"}}`: "",
		`{"Peers": {"inputs": ["Peer"], "program": "/nix/store/x/bin/v"}}`:          "validator Peers: the name is not lower-case letters, digits and dashes",
		`{"peers": {"inputs": ["Peer"], "program": "v"}}`:                           `the program "v" names no program by its absolute path`,
		`{"peers": {"inputs": [], "program": "/nix/store/x/bin/v"}}`:                "it reads no kind",
		`{"peers": {"inputs": ["Secret"], "program": "/nix/store/x/bin/v"}}`:        "it reads Secret documents, whose values no program reads",
	} {
		path := filepath.Join(dir, "validators.json")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		defs, err := ReadValidators(path)
		switch {
		case want == "" && (err != nil || defs["peers"].Program != "/nix/store/x/bin/v --strict"):
			t.Errorf("%s: %v, %v", text, defs, err)
		case want != "" && (err == nil || !strings.HasSuffix(err.Error(), want)):
			t.Errorf("%s: %v, want an error ending in %q", text, err, want)
		}
	}
}

// A linked validator reads what a validator's program reads, and its refusals are held to the
// format a program's report is: a refusal outside it, or an error, fails the validator. A message
// that quotes a rune refusals.Unprintable reports stays a refusal, with a space for the rune.
func TestRunLinked(t *testing.T) {
	var read []byte
	taken := refusals.Refusal{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}
	linked := []LinkedValidator{
		{Name: "reads", Inputs: []string{"Demo"}, Validate: func(stdin []byte) ([]refusals.Refusal, error) {
			read = stdin
			return []refusals.Refusal{taken, taken}, nil
		}},
		{Name: "errs", Inputs: []string{"Demo"}, Validate: func([]byte) ([]refusals.Refusal, error) {
			return nil, errors.New("the input holds no documents")
		}},
		{Name: "malformed", Inputs: []string{"Demo"}, Validate: func([]byte) ([]refusals.Refusal, error) {
			return []refusals.Refusal{{Kind: "Demo", Name: "one", Path: "spec name", Message: "is taken"}}, nil
		}},
		{Name: "quotes", Inputs: []string{"Demo"}, Validate: func([]byte) ([]refusals.Refusal, error) {
			return []refusals.Refusal{{Kind: "Demo", Name: "two", Path: "spec.name", Message: "two\nlines\u202e"}}, nil
		}},
	}
	in := input(t, demos)
	failures := RunLinked(linked, in)
	want := []Failure{
		{Name: "reads", Role: RoleValidator, Problem: "its program refused the config: Demo/one: spec.name: is taken", Refusals: []refusals.Refusal{taken}},
		{Name: "errs", Role: RoleValidator, Problem: "its program failed: the input holds no documents"},
		{Name: "malformed", Role: RoleValidator, Problem: `its program's report: refusal 1: path "spec name" is no field's path`},
		{Name: "quotes", Role: RoleValidator, Problem: "its program refused the config: Demo/two: spec.name: two lines ", Refusals: []refusals.Refusal{{Kind: "Demo", Name: "two", Path: "spec.name", Message: "two lines "}}},
	}
	if !slices.EqualFunc(failures, want, sameFailure) {
		t.Errorf("failures = %#v", failures)
	}
	if got := Message(failures); got != "the config is refused: Demo/one: spec.name: is taken; Demo/two: spec.name: two lines ; validator errs: its program failed: the input holds no documents; validator malformed: its program's report: refusal 1: path \"spec name\" is no field's path" {
		t.Errorf("Message = %q", got)
	}
	stdin, err := in.stdin([]string{"Demo"})
	if err != nil || string(read) != string(stdin) {
		t.Errorf("the linked validator read %s, a program %s: %v", read, stdin, err)
	}
}

// A directory chalkd cannot prepare for its validators is an error of chalkd's own, which fails
// no validator and refuses nothing.
func TestValidateDirectoryError(t *testing.T) {
	rec := &recorder{}
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := Renderer{Root: root, Run: rec}
	defs := map[string]Validator{"peers": {Inputs: []string{"Demo"}, Program: "/nix/store/x/bin/v"}}
	failures, err := r.Validate(context.Background(), defs, input(t, demos))
	if err == nil || failures != nil || len(rec.programs) != 0 {
		t.Errorf("Validate = %v, %v; programs %v", failures, err, rec.programs)
	}
}
