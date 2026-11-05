// Command chalkos-rules checks an image when it is built: every change rule its schemas declare
// and every rule of its effects compiles, within the cost limit, and reads only facts the image
// declares, each a file or a program of the image's store; every effect acts on units the image
// defines, outputs it declares and kinds its schemas declare; every output renders its examples
// and passes its check; every validator accepts its examples; and every diagnostic is one chalkd
// can run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/command"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/withheld"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/validators"
)

const usage = `usage: chalkos-rules check IMAGE

Checks the files of the directory IMAGE, as an image's build lays them out: schemas/, the
directory of its schemas; facts.json, effects.json, outputs.json, validators.json and
diagnostics.json, the image's own; examples.json and validator-examples.json, the examples
directory of each output and of each validator by name; and units/, its systemd units. Every
rule of the schemas and of the effects compiles and reads only facts facts.json declares, every
unit an effect restarts or reloads is one of units/, every effect's outputs and kinds name an
output or a kind that exists, every output and validator reads kinds the schemas declare, each
output renders each of its examples and passes its check, each validator accepts each of its
examples, and every diagnostic is one chalkd can run.`

func main() {
	if len(os.Args) != 3 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := check(os.Args[2], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "chalkos-rules: "+err.Error())
		os.Exit(1)
	}
}

func check(image string, out io.Writer) error {
	s, err := schema.LoadSchemas(filepath.Join(image, "schemas"))
	if err != nil {
		return err
	}
	defs, err := facts.Read(filepath.Join(image, "facts.json"))
	if err != nil {
		return err
	}
	effectDefs, err := effects.Read(filepath.Join(image, "effects.json"))
	if err != nil {
		return err
	}
	outs, err := outputs.Read(filepath.Join(image, "outputs.json"), outputs.DefaultRoot)
	if err != nil {
		return err
	}
	if _, err := diagnostics.Read(filepath.Join(image, "diagnostics.json")); err != nil {
		return err
	}
	examples := map[string]string{}
	if err := jsonfile.Read(filepath.Join(image, "examples.json"), &examples); err != nil {
		return err
	}
	vals, err := validators.Read(filepath.Join(image, "validators.json"))
	if err != nil {
		return err
	}
	valExamples := map[string]string{}
	if err := jsonfile.Read(filepath.Join(image, "validator-examples.json"), &valExamples); err != nil {
		return err
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		if err := defs[name].Check(); err != nil {
			errs = append(errs, fmt.Errorf("fact %s: %w", name, err))
		}
	}
	set, err := rules.Compile(s)
	if err != nil {
		errs = append(errs, err)
	}
	read := map[string]bool{}
	reads := func(where, rule string, names []string) {
		for _, f := range names {
			if _, ok := defs[f]; !ok {
				errs = append(errs, fmt.Errorf("%s: rule %q reads facts.%s, which the image does not declare; it declares %s", where, rule, f, declared(defs)))
			}
			read[f] = true
		}
	}
	if set != nil {
		for _, p := range set.Programs {
			reads(p.Where(), p.Rule.Rule, p.Facts)
		}
	}
	compiled := 0
	for _, name := range slices.Sorted(maps.Keys(effectDefs)) {
		e := effectDefs[name]
		for _, unit := range slices.Concat(e.Restart, e.Reload) {
			if err := checkUnit(image, unit); err != nil {
				errs = append(errs, fmt.Errorf("effect %s: %w", name, err))
			}
		}
		if e.When == "" {
			continue
		}
		p, err := rules.CompileEffect(name, e.When)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		reads(p.Where(), p.When, p.Facts)
		compiled++
	}
	errs = append(errs, checkOutputs(s, outs, effectDefs)...)
	errs = append(errs, checkValidators(s, vals)...)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// The examples render, and the validators run on theirs, once the outputs and the validators
	// are sound.
	rendered, exampleErrs := checkExamples(s, outs, examples)
	validated, validatorErrs := checkValidatorExamples(s, vals, valExamples)
	if exampleErrs = append(exampleErrs, validatorErrs...); len(exampleErrs) > 0 {
		return errors.Join(exampleErrs...)
	}
	fmt.Fprintf(out, "%d rules and %d effect rules compile, reading the facts %s; %d outputs render %d examples; %d validators accept %d examples\n", len(set.Programs), compiled, declared(read), len(outs), rendered, len(vals), validated)
	return nil
}

// checkUnit refuses a unit the image does not define, which a delivery could not restart or
// reload.
func checkUnit(image, unit string) error {
	if _, err := os.Stat(filepath.Join(image, "units", unit)); err != nil {
		hint := ""
		if !strings.Contains(unit, ".") {
			hint = fmt.Sprintf("; did you mean %s.service?", unit)
		}
		return fmt.Errorf("the image defines no unit %s%s", unit, hint)
	}
	return nil
}

func declared[V any](m map[string]V) string {
	names := slices.Sorted(maps.Keys(m))
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// checkOutputs refuses:
//   - an output whose programs are not of the image's store;
//   - an output whose inputs are kinds the image does not declare, or that a unit cannot read;
//   - an effect whose outputs name no output, or whose kinds are not the image's.
func checkOutputs(s *schema.Schemas, defs map[string]outputs.Definition, effectDefs map[string]effects.Definition) []error {
	kinds := map[string]bool{}
	for _, k := range s.Kinds() {
		kinds[k.Kind] = true
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		d := defs[name]
		for _, p := range []struct{ what, command string }{{"renderer", d.Render}, {"check", d.Check}} {
			// InStore judges the program, the first word, alone: an argument such as /var/lib/
			// need not be a clean path.
			if argv := strings.Fields(p.command); p.command != "" && (len(argv) == 0 || !command.InStore(argv[0])) {
				errs = append(errs, fmt.Errorf("output %s: the %s %q is no program of the image's store", name, p.what, p.command))
			}
		}
		for _, kind := range d.Inputs {
			switch {
			case !kinds[kind]:
				errs = append(errs, fmt.Errorf("output %s: the image declares no kind %s", name, kind))
			case withheld.Kind(kind):
				errs = append(errs, fmt.Errorf("output %s: a renderer does not read %s documents", name, kind))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(effectDefs)) {
		for _, o := range effectDefs[name].Outputs {
			if _, ok := defs[o]; !ok {
				errs = append(errs, fmt.Errorf("effect %s: outputs names %s, which is no output of the image; its outputs are %s", name, o, declared(defs)))
			}
		}
		for _, kind := range effectDefs[name].Kinds {
			switch {
			case withheld.Kind(kind):
				errs = append(errs, fmt.Errorf("effect %s: an effect does not fire on %s documents", name, kind))
			case !kinds[kind]:
				errs = append(errs, fmt.Errorf("effect %s: the image declares no kind %s", name, kind))
			}
		}
	}
	return errs
}

// checkExamples renders each output from each file of its examples directory, a config set of
// its own, as the node example, and runs its check, as a node would; it returns the number of
// examples rendered. The build runs the programs as they are, and leaves the files' owner.
func checkExamples(s *schema.Schemas, defs map[string]outputs.Definition, examples map[string]string) (int, []error) {
	var errs []error
	n := 0
	for _, name := range slices.Sorted(maps.Keys(examples)) {
		d, ok := defs[name]
		if !ok {
			errs = append(errs, fmt.Errorf("output %s: it has examples but is no output", name))
			continue
		}
		files, err := exampleFiles(examples[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("output %s: its examples: %w", name, err))
			continue
		}
		for _, file := range files {
			if err := renderExample(s, name, d, file); err != nil {
				errs = append(errs, fmt.Errorf("output %s: example %s: %w", name, filepath.Base(file), err))
			}
			n++
		}
	}
	return n, errs
}

// exampleFiles are the YAML and JSON files of dir, sorted; a directory without any is refused.
func exampleFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".yaml", ".yml", ".json":
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no YAML or JSON file", dir)
	}
	return files, nil
}

// exampleDocuments are the documents of an example file, each valid against the schemas and with
// its defaults, as a node's renderers and validators read them.
func exampleDocuments(s *schema.Schemas, file string) ([]config.Document, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	docs, err := config.Parse(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	for i, doc := range docs {
		if err := s.Validate(doc.Object); err != nil {
			return nil, err
		}
		filled, err := s.Default(doc.Object)
		if err != nil {
			return nil, err
		}
		docs[i].Object = filled
	}
	return docs, nil
}

func renderExample(s *schema.Schemas, name string, d outputs.Definition, file string) error {
	docs, err := exampleDocuments(s, file)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "chalkos-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	r := outputs.Renderer{Root: root, Run: outputs.Direct{}, KeepOwner: true}
	_, failures := r.Render(context.Background(), map[string]outputs.Definition{name: d}, outputs.Input{Node: "example", Documents: docs, Schemas: s})
	if len(failures) > 0 {
		// What the program wrote to standard error is in the build's log, above.
		return errors.New(strings.TrimSuffix(failures[0].Problem, "; see the node's journal"))
	}
	return nil
}

// checkValidators refuses a validator whose program is not of the image's store, or whose inputs
// are kinds the image does not declare; validators.Read refused the rest already.
func checkValidators(s *schema.Schemas, defs map[string]validators.Definition) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		d := defs[name]
		// InStore judges the program, the first word, alone, as for an output's.
		if argv := strings.Fields(d.Program); !command.InStore(argv[0]) {
			errs = append(errs, fmt.Errorf("validator %s: the program %q is no program of the image's store", name, d.Program))
		}
		for _, kind := range d.Inputs {
			if !slices.ContainsFunc(s.Kinds(), func(k schema.Kind) bool { return k.Kind == kind }) {
				errs = append(errs, fmt.Errorf("validator %s: the image declares no kind %s", name, kind))
			}
		}
	}
	return errs
}

// checkValidatorExamples runs each validator on each file of its examples directory, a config
// set of its own, as a node would, and refuses an example the validator refuses or fails on; it
// returns the number of examples run. The build runs the programs as they are.
func checkValidatorExamples(s *schema.Schemas, defs map[string]validators.Definition, examples map[string]string) (int, []error) {
	var errs []error
	n := 0
	for _, name := range slices.Sorted(maps.Keys(examples)) {
		d, ok := defs[name]
		if !ok {
			errs = append(errs, fmt.Errorf("validator %s: it has examples but is no validator", name))
			continue
		}
		files, err := exampleFiles(examples[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("validator %s: its examples: %w", name, err))
			continue
		}
		for _, file := range files {
			if err := validateExample(s, name, d, file); err != nil {
				errs = append(errs, fmt.Errorf("validator %s: example %s: %w", name, filepath.Base(file), err))
			}
			n++
		}
	}
	return n, errs
}

func validateExample(s *schema.Schemas, name string, d validators.Definition, file string) error {
	docs, err := exampleDocuments(s, file)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "chalkos-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	r := outputs.Renderer{Root: root, Run: outputs.Direct{}}
	rs, failures, err := validators.Run(context.Background(), r, map[string]validators.Definition{name: d}, outputs.Input{Node: "example", Documents: docs, Schemas: s})
	if err != nil {
		return err
	}
	if len(rs) > 0 || len(failures) > 0 {
		// What the program wrote to standard error is in the build's log, above.
		return errors.New(strings.TrimSuffix(validators.Refusal(rs, failures), "; see the node's journal"))
	}
	return nil
}
