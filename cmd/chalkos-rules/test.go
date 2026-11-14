package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/rules"
)

// testFile is tests.json: the image's tests by name.
type testFile struct {
	Tests map[string]testCase `json:"tests"`
}

// testCase is a test as chalkos.tests declares it: the config, and the config before it for a
// test of a change; the node's name and facts, as they are and as they were before; and what the
// test expects: the files of the outputs named, by output; the refusals, always checked, none
// when null, as for a config the test expects accepted; and the names of the effects due, left
// unchecked when null.
type testCase struct {
	Config   string             `json:"config"`
	Before   string             `json:"before"`
	Node     string             `json:"node"`
	Facts    map[string]any     `json:"facts"`
	OldFacts map[string]any     `json:"oldFacts"`
	Outputs  map[string]string  `json:"outputs"`
	Refusals []refusals.Refusal `json:"refusals"`
	Effects  *[]string          `json:"effects"`
}

// givenFacts are a test's facts, which a rule reads as facts.<name>; one it does not give is null.
type givenFacts map[string]any

func (f givenFacts) Fact(name string) (any, error) { return f[name], nil }

// factsOf gives the facts f to every rule of a check.
func factsOf(f map[string]any) func(_, _ config.Redacted) rules.Facts {
	return func(_, _ config.Redacted) rules.Facts { return givenFacts(f) }
}

// storeHash matches a store path's hash, which a test's golden files hold as eeee…, so they stay
// the same when a dependency changes.
var storeHash = regexp.MustCompile(`/nix/store/[0-9a-df-np-sv-z]{32}-`)

const normalisedStore = "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-"

// reservedNames are the files of OUT itself, which no test's directory may be.
var reservedNames = []string{"summary.txt", "failures", "goldens.tsv"}

// runTests runs the tests of the file testsPath against the image of the directory image and
// writes what each got below out, which should be new or empty, as files of an earlier run stay:
// OUT/<test>/outputs/<output>/, refusals.json, effects.json and warnings; summary.txt, a PASS or
// FAIL line per test and a WARN line per warning; failures, the FAIL lines alone; and
// goldens.tsv, a line per output whose files differ from its golden files: the test, the output,
// the golden files' directory and the rendered files' directory below out. A test that fails is
// no error: an error is an image or a file of tests the runner cannot read.
func runTests(image, testsPath, out string) error {
	c, err := contract.Load(image)
	if err != nil {
		return err
	}
	set, err := c.Rules()
	if err != nil {
		return err
	}
	effectRules, err := c.EffectRules()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(testsPath)
	if err != nil {
		return err
	}
	var tf testFile
	if err := jsonfile.DecodeStrict(data, &tf); err != nil {
		return fmt.Errorf("%s: %w", testsPath, err)
	}
	for name := range tf.Tests {
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || slices.Contains(reservedNames, name) || strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("%s: the test %q is no name of a directory of its own", testsPath, name)
		}
	}
	root, err := os.MkdirTemp("", "chalkos-tests-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	// The build runs the programs as they are, and leaves the files' owner.
	render := outputs.Renderer{Root: root, Run: outputs.Direct{}, KeepOwner: true}
	r := tester{
		img: check.Image{
			Schemas: c.Schemas, ChangeRules: set,
			Effects:    check.Effects{Defs: c.Effects, Rules: effectRules},
			Validators: check.Programs{Defs: c.Validators, Render: render},
			Outputs:    c.Outputs, Renderer: render,
		},
		facts: c.Facts,
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var summary, failures, goldens strings.Builder
	for _, name := range slices.Sorted(maps.Keys(tf.Tests)) {
		got, err := r.run(context.Background(), filepath.Join(out, name), tf.Tests[name])
		if err != nil {
			return fmt.Errorf("test %s: %w", name, err)
		}
		if len(got.problems) == 0 {
			fmt.Fprintf(&summary, "PASS %s\n", name)
		}
		for _, p := range got.problems {
			line := fmt.Sprintf("FAIL %s: %s\n", name, p)
			summary.WriteString(line)
			failures.WriteString(line)
		}
		for _, w := range got.warnings {
			fmt.Fprintf(&summary, "WARN %s: %s\n", name, w)
		}
		for _, o := range got.goldens {
			fmt.Fprintf(&goldens, "%s\t%s\t%s\t%s\n", name, o, tf.Tests[name].Outputs[o], filepath.Join(name, "outputs", o))
		}
	}
	for file, text := range map[string]string{"summary.txt": summary.String(), "failures": failures.String(), "goldens.tsv": goldens.String()} {
		if err := os.WriteFile(filepath.Join(out, file), []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// tester runs tests against an image: img checks their configs, and facts are the facts it
// declares, which a test's facts name.
type tester struct {
	img   check.Image
	facts map[string]facts.Definition
}

// testResult is what a test got wrong: its problems, in the order the summary lists them, and
// the outputs whose files differ from their golden files; and the warnings of its check.
type testResult struct {
	problems []string
	goldens  []string
	warnings []string
}

// run runs tc and writes what it got below dir. An error is one of writing dir.
func (t tester) run(ctx context.Context, dir string, tc testCase) (testResult, error) {
	var r testResult
	for _, given := range []struct {
		field string
		facts map[string]any
	}{{"facts", tc.Facts}, {"oldFacts", tc.OldFacts}} {
		for _, name := range slices.Sorted(maps.Keys(given.facts)) {
			if _, ok := t.facts[name]; !ok {
				r.problems = append(r.problems, fmt.Sprintf("%s names %s, which the image does not declare; it declares %s", given.field, name, declared(t.facts)))
			}
		}
	}
	docs, err := readConfig(tc.Config)
	if err != nil {
		r.problems = append(r.problems, "the check failed: "+err.Error())
		return r, nil
	}
	in := check.Input{Node: tc.Node, Delivered: docs, Facts: factsOf(tc.Facts)}
	if tc.Before != "" {
		cmp, before, problem := t.before(ctx, tc)
		if problem != "" {
			r.problems = append(r.problems, problem)
			return r, nil
		}
		in.Old, in.Compare = before, []check.Comparison{cmp}
	}
	res, runErr := t.check(ctx, in)
	got, err := refusalsOf(runErr)
	if err != nil {
		r.problems = append(r.problems, "the check failed: "+err.Error())
		return r, nil
	}
	rendered := map[string]bool{}
	if res.Staged != nil {
		defer res.Staged.Discard()
		for name, staged := range res.Staged.Dirs() {
			if err := copyNormalised(staged, filepath.Join(dir, "outputs", name)); err != nil {
				return r, fmt.Errorf("output %s: %w", name, err)
			}
			rendered[name] = true
		}
	}
	effects := []string{}
	for _, e := range res.Plan.Effects {
		effects = append(effects, e.Name)
	}
	// A warning is one line of the summary and of the warnings file.
	for _, w := range res.Warnings {
		r.warnings = append(r.warnings, strings.ReplaceAll(w, "\n", " "))
	}
	if err := writeJSON(filepath.Join(dir, "refusals.json"), got); err != nil {
		return r, err
	}
	if err := writeJSON(filepath.Join(dir, "effects.json"), effects); err != nil {
		return r, err
	}
	if len(r.warnings) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "warnings"), []byte(strings.Join(r.warnings, "\n")+"\n"), 0o644); err != nil {
			return r, err
		}
	}
	for _, o := range slices.Sorted(maps.Keys(tc.Outputs)) {
		switch {
		case !rendered[o] && len(got) > 0:
			r.problems = append(r.problems, fmt.Sprintf("output %s rendered nothing; the config was refused", o))
		case !rendered[o]:
			r.problems = append(r.problems, fmt.Sprintf("output %s rendered nothing; the image declares no output %s", o, o))
		default:
			same, irregular, err := sameFiles(tc.Outputs[o], filepath.Join(dir, "outputs", o))
			if err != nil {
				return r, fmt.Errorf("output %s: %w", o, err)
			}
			switch {
			case irregular != "":
				// Taking the rendered files would not mend it, so goldens.tsv does not list it.
				r.problems = append(r.problems, fmt.Sprintf("output %s: its golden files hold %s, which is no regular file", o, irregular))
			case !same:
				r.problems = append(r.problems, fmt.Sprintf("output %s differs from its golden files", o))
				r.goldens = append(r.goldens, o)
			}
		}
	}
	want := tc.Refusals
	if want == nil {
		want = []refusals.Refusal{}
	}
	if !slices.Equal(want, got) {
		r.problems = append(r.problems, fmt.Sprintf("refusals differ: expected %s, got %s", compact(want), compact(got)))
	}
	if tc.Effects != nil && !slices.Equal(*tc.Effects, effects) {
		r.problems = append(r.problems, fmt.Sprintf("effects differ: expected %s, got %s", compact(*tc.Effects), compact(effects)))
	}
	return r, nil
}

// before checks the config before tc's change, with the facts as they were, and returns what the
// effects compare tc's config with, and the documents before as delivered; or the problem of a
// before that is refused, or whose check failed.
func (t tester) before(ctx context.Context, tc testCase) (check.Comparison, config.Redacted, string) {
	before, err := readConfig(tc.Before)
	if err != nil {
		return check.Comparison{}, config.Redacted{}, "the check of before failed: " + err.Error()
	}
	prev, err := t.check(ctx, check.Input{Node: tc.Node, Delivered: before, Facts: factsOf(tc.OldFacts)})
	if _, refused := errors.AsType[*check.Refusal](err); refused {
		return check.Comparison{}, config.Redacted{}, "before is refused: " + err.Error()
	}
	if err != nil {
		return check.Comparison{}, config.Redacted{}, "the check of before failed: " + err.Error()
	}
	defer prev.Staged.Discard()
	kinds, err := config.KindHashes(prev.Documents, t.img.Schemas)
	if err != nil {
		return check.Comparison{}, config.Redacted{}, "the check of before failed: " + err.Error()
	}
	return check.Comparison{
		Documents: prev.Documents, Facts: givenFacts(tc.Facts), OldFacts: givenFacts(tc.OldFacts),
		Outputs: prev.Staged.States(), Kinds: kinds,
	}, before, ""
}

// check is check.Run without the translation into the node's identity, which chalkctl validate
// checks, so a test holds the documents it is about alone: it checks in.Delivered as a set with
// Image.CheckRedacted and runs check.RunTranslated.
func (t tester) check(ctx context.Context, in check.Input) (check.Result, error) {
	docs, err := t.img.CheckRedacted(in.Delivered)
	if err != nil {
		return check.Result{}, err
	}
	return check.RunTranslated(ctx, t.img, in, docs)
}

// readConfig reads the documents of the YAML or JSON file at path, redacted, as a delivery gives
// them to a node.
func readConfig(path string) (config.Redacted, error) {
	f, err := os.Open(path)
	if err != nil {
		return config.Redacted{}, err
	}
	defer f.Close()
	docs, err := config.Parse(f)
	if err != nil {
		return config.Redacted{}, fmt.Errorf("%s: %w", path, err)
	}
	return config.Redact(docs), nil
}

// refusalsOf are the refusals of err, an error of tester.check, in the order chalkctl prints them,
// each once: none for no error, and for a failure without refusals, a refusal of its message
// alone. An error that is no *check.Refusal is returned.
func refusalsOf(err error) ([]refusals.Refusal, error) {
	got := []refusals.Refusal{}
	if err == nil {
		return got, nil
	}
	r, ok := errors.AsType[*check.Refusal](err)
	if !ok {
		return nil, err
	}
	add := func(rs ...refusals.Refusal) {
		for _, x := range rs {
			if !slices.Contains(got, x) {
				got = append(got, x)
			}
		}
	}
	switch {
	case r.Stage == check.StageRules && len(r.Failures) > 0:
		for _, f := range r.Failures {
			add(f.Refusal)
		}
	case (r.Stage == check.StageValidators || r.Stage == check.StageOutputs) && len(r.Failed) > 0:
		// outputs.Message reports the refusals first, then each other failure. What a program
		// wrote to standard error is in the log of the run.
		for _, f := range r.Failed {
			add(f.Refusals...)
		}
		for _, f := range r.Failed {
			if len(f.Refusals) == 0 {
				add(refusals.Refusal{Message: f.OffNode()})
			}
		}
	default:
		add(refusals.Refusal{Message: r.Message})
	}
	return got, nil
}

// copyNormalised copies the files below from, which a renderer wrote and so are regular, to to,
// each with mode 0644 and with each store path's hash replaced as storeHash says.
func copyNormalised(from, to string) error {
	return filepath.WalkDir(from, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, storeHash.ReplaceAll(data, []byte(normalisedStore)), 0o644)
	})
}

// sameFiles tells whether the directories golden and got hold the same files, by relative path,
// with the same content; a missing directory holds none. A directory that holds anything but
// directories and regular files, such as a link, differs from every directory: irregular is the
// first such file of golden, by relative path.
func sameFiles(golden, got string) (same bool, irregular string, err error) {
	fa, irregular, err := filesOf(golden)
	if err != nil || irregular != "" {
		return false, irregular, err
	}
	fb, other, err := filesOf(got)
	if err != nil {
		return false, "", err
	}
	return other == "" && maps.EqualFunc(fa, fb, bytes.Equal), "", nil
}

// filesOf are the contents of the regular files below dir, by relative path, and the first file
// below dir, in lexical order, that is neither a directory nor a regular file; empty for none.
func filesOf(dir string) (map[string][]byte, string, error) {
	files := map[string][]byte{}
	irregular := ""
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == dir {
			return filepath.SkipDir
		}
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if !e.Type().IsRegular() {
			if irregular == "" {
				irregular = rel
			}
			return nil
		}
		files[rel], err = os.ReadFile(path)
		return err
	})
	return files, irregular, err
}

// writeJSON writes v to path as compact JSON with a trailing newline.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(compact(v)+"\n"), 0o644)
}

// compact is v as compact JSON.
func compact(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		// Refusals and effect names are strings alone.
		panic(err)
	}
	return string(data)
}
