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

	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/rules"
)

// testFile is tests.json: the image's platform, which only the translation into a node's identity
// reads, and its tests by name.
type testFile struct {
	Platform string              `json:"platform"`
	Tests    map[string]testCase `json:"tests"`
}

// testCase is a test as chalkos.tests declares it: the config, and the config before it for a
// test of a change; the node's name and facts, as they are and as they were before; and what the
// test expects, each left unchecked when null: the files of the outputs named, by output, the
// refusals, and the names of the effects due.
type testCase struct {
	Config   string              `json:"config"`
	Before   string              `json:"before"`
	Node     string              `json:"node"`
	Facts    map[string]any      `json:"facts"`
	OldFacts map[string]any      `json:"oldFacts"`
	Outputs  map[string]string   `json:"outputs"`
	Refusals *[]refusals.Refusal `json:"refusals"`
	Effects  *[]string           `json:"effects"`
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

// runTests runs the tests of the file testsPath against the image of the directory image and
// writes what each got below out: OUT/<test>/outputs/<output>/, refusals.json and effects.json;
// summary.txt, a PASS or FAIL line per test; failures, the FAIL lines alone; and goldens.tsv, a
// line per output whose files differ from its golden files: the test, the output, the golden
// files' directory and the rendered files' directory below out. A test that fails is no error:
// an error is an image or a file of tests the runner cannot read.
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
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
			return fmt.Errorf("%s: the test %q is no file name", testsPath, name)
		}
	}
	root, err := os.MkdirTemp("", "chalkos-tests-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	// The build runs the programs as they are, and leaves the files' owner.
	render := outputs.Renderer{Root: root, Run: outputs.Direct{}, KeepOwner: true}
	img := check.Image{
		Schemas: c.Schemas, ChangeRules: set,
		Effects:    check.Effects{Defs: c.Effects, Rules: effectRules},
		Validators: check.Programs{Defs: c.Validators, Render: render},
		Outputs:    c.Outputs, Renderer: render,
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var summary, failures, goldens strings.Builder
	for _, name := range slices.Sorted(maps.Keys(tf.Tests)) {
		r, err := runTest(context.Background(), img, filepath.Join(out, name), tf.Tests[name])
		if err != nil {
			return fmt.Errorf("test %s: %w", name, err)
		}
		if len(r.problems) == 0 {
			fmt.Fprintf(&summary, "PASS %s\n", name)
		}
		for _, p := range r.problems {
			line := fmt.Sprintf("FAIL %s: %s\n", name, p)
			summary.WriteString(line)
			failures.WriteString(line)
		}
		for _, o := range r.goldens {
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

// testResult is what a test got wrong: its problems, in the order the summary lists them, and
// the outputs whose files differ from their golden files.
type testResult struct {
	problems []string
	goldens  []string
}

// runTest runs tc against img and writes what it got below dir. An error is one of writing dir.
func runTest(ctx context.Context, img check.Image, dir string, tc testCase) (testResult, error) {
	var r testResult
	docs, err := readConfig(tc.Config)
	if err != nil {
		r.problems = append(r.problems, "the check failed: "+err.Error())
		return r, nil
	}
	in := check.Input{Node: tc.Node, Delivered: docs, Facts: factsOf(tc.Facts)}
	if tc.Before != "" {
		cmp, before, problem := runBefore(ctx, img, tc)
		if problem != "" {
			r.problems = append(r.problems, problem)
			return r, nil
		}
		in.Old, in.Compare = before, []check.Comparison{cmp}
	}
	res, runErr := runChecks(ctx, img, in)
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
	if err := writeJSON(filepath.Join(dir, "refusals.json"), got); err != nil {
		return r, err
	}
	if err := writeJSON(filepath.Join(dir, "effects.json"), effects); err != nil {
		return r, err
	}
	for _, o := range slices.Sorted(maps.Keys(tc.Outputs)) {
		switch {
		case !rendered[o] && len(got) > 0:
			r.problems = append(r.problems, fmt.Sprintf("output %s rendered nothing; the config was refused", o))
		case !rendered[o]:
			r.problems = append(r.problems, fmt.Sprintf("output %s rendered nothing; the image declares no output %s", o, o))
		default:
			same, err := sameFiles(tc.Outputs[o], filepath.Join(dir, "outputs", o))
			if err != nil {
				return r, fmt.Errorf("output %s: %w", o, err)
			}
			if !same {
				r.problems = append(r.problems, fmt.Sprintf("output %s differs from its golden files", o))
				r.goldens = append(r.goldens, o)
			}
		}
	}
	if tc.Refusals != nil && !slices.Equal(*tc.Refusals, got) {
		r.problems = append(r.problems, fmt.Sprintf("refusals differ: expected %s, got %s", compact(*tc.Refusals), compact(got)))
	}
	if tc.Effects != nil && !slices.Equal(*tc.Effects, effects) {
		r.problems = append(r.problems, fmt.Sprintf("effects differ: expected %s, got %s", compact(*tc.Effects), compact(effects)))
	}
	return r, nil
}

// runBefore checks the config before tc's change, with the facts as they were, and returns what
// the effects compare tc's config with, and the documents before as delivered; or the problem of a
// before that is refused.
func runBefore(ctx context.Context, img check.Image, tc testCase) (check.Comparison, config.Redacted, string) {
	before, err := readConfig(tc.Before)
	if err != nil {
		return check.Comparison{}, config.Redacted{}, "before is refused: " + err.Error()
	}
	prev, err := runChecks(ctx, img, check.Input{Node: tc.Node, Delivered: before, Facts: factsOf(tc.OldFacts)})
	if err != nil {
		return check.Comparison{}, config.Redacted{}, "before is refused: " + err.Error()
	}
	defer prev.Staged.Discard()
	kinds, err := config.KindHashes(prev.Documents, img.Schemas)
	if err != nil {
		return check.Comparison{}, config.Redacted{}, "before is refused: " + err.Error()
	}
	return check.Comparison{
		Documents: prev.Documents, Facts: givenFacts(tc.Facts), OldFacts: givenFacts(tc.OldFacts),
		Outputs: prev.Staged.States(), Kinds: kinds,
	}, before, ""
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

// refusalsOf are the refusals of err, an error of runChecks, in the order chalkctl prints them,
// each once: none for no error, and for a failure without refusals, a refusal of its message
// alone. An error that is no *check.Refusal is returned.
func refusalsOf(err error) ([]refusals.Refusal, error) {
	got := []refusals.Refusal{}
	if err == nil {
		return got, nil
	}
	var r *check.Refusal
	if !errors.As(err, &r) {
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
		// outputs.Message reports the refusals first, then each other failure.
		for _, f := range r.Failed {
			add(f.Refusals...)
		}
		// What a program wrote to standard error is in the log of the run, and in no journal.
		for _, f := range r.Failed {
			if len(f.Refusals) == 0 {
				add(refusals.Refusal{Message: strings.TrimSuffix(f.Error(), "; see the node's journal")})
			}
		}
	default:
		add(refusals.Refusal{Message: r.Message})
	}
	return got, nil
}

// copyNormalised copies the regular files below from to to, with each store path's hash replaced
// as storeHash says.
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

// sameFiles tells whether the directories a and b hold the same files, by relative path, with the
// same content; a missing directory holds none.
func sameFiles(a, b string) (bool, error) {
	fa, err := filesOf(a)
	if err != nil {
		return false, err
	}
	fb, err := filesOf(b)
	if err != nil {
		return false, err
	}
	return maps.EqualFunc(fa, fb, bytes.Equal), nil
}

// filesOf are the contents of the files below dir, by relative path.
func filesOf(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == dir {
			return filepath.SkipDir
		}
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[rel], err = os.ReadFile(path)
		return err
	})
	return files, err
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

// runChecks is check.Run without the translation into the node's identity, which chalkctl
// validate checks, so a test holds the documents it is about alone: it checks in.Delivered as a
// set with config.CheckRedacted, which refuses as StageConfig, and then runs the image's change
// rules, validators and renderers and plans the effects, as Run does.
func runChecks(ctx context.Context, img check.Image, in check.Input) (check.Result, error) {
	docs, err := config.CheckRedacted(in.Delivered, img.Schemas)
	if err != nil {
		return check.Result{}, &check.Refusal{Stage: check.StageConfig, Message: err.Error()}
	}
	res := check.Result{Documents: docs}
	old := in.Old.Defaulted(img.Schemas)
	if err := img.Rules(rules.Input{Documents: docs, Old: old, Node: in.Node, Facts: in.Facts(docs, old)}); err != nil {
		return res, err
	}
	if err := img.Validate(ctx, in.Node, docs); err != nil {
		return res, err
	}
	staged, err := img.Render(ctx, in.Node, docs, true)
	if err != nil {
		return res, err
	}
	o, warnings, err := check.Due(img.Effects, check.Delivery{Node: in.Node, Documents: docs, Schemas: img.Schemas, Outputs: staged.States()}, in.Compare)
	if err != nil {
		staged.Discard()
		return res, err
	}
	res.Staged, res.Plan, res.Warnings = staged, check.PlanOf(img.Effects, o), warnings
	return res, nil
}
