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
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

const usage = `usage: chalkos-rules check IMAGE

Checks the files of the directory IMAGE, as an image's build lays them out: schemas/, the
directory of its schemas; facts.json, effects.json, outputs.json, validators.json and
diagnostics.json, the image's own; examples.json, {"outputs": {...}, "validators": {...}}, the
examples directory of each output and of each validator by name; and units/, its systemd units.
Every rule of the schemas and of the effects compiles and reads only facts facts.json declares,
every unit an effect restarts or reloads is one of units/, every effect's outputs and kinds name
an output or a kind that exists, every output and validator reads kinds the schemas declare,
each output renders each of its examples and passes its check, each validator accepts each of
its examples, and every diagnostic is one chalkd can run.`

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
	vals, err := outputs.ReadValidators(filepath.Join(image, "validators.json"))
	if err != nil {
		return err
	}
	var ex examples
	if err := jsonfile.Read(filepath.Join(image, "examples.json"), &ex); err != nil {
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
	for _, name := range slices.Sorted(maps.Keys(outs)) {
		errs = append(errs, checkPrograms(s, outputs.RoleOutput, name, outs[name].Inputs, map[string]string{"renderer": outs[name].Render, "check": outs[name].Check})...)
	}
	for _, name := range slices.Sorted(maps.Keys(vals)) {
		errs = append(errs, checkPrograms(s, outputs.RoleValidator, name, vals[name].Inputs, map[string]string{"program": vals[name].Program})...)
	}
	errs = append(errs, checkEffects(s, outs, effectDefs)...)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// The outputs and the validators run on their examples once they are sound.
	ran, exampleErrs := checkExamples(s, outs, vals, ex)
	if len(exampleErrs) > 0 {
		return errors.Join(exampleErrs...)
	}
	fmt.Fprintf(out, "%d rules and %d effect rules compile, reading the facts %s; %d outputs render %d examples; %d validators accept %d examples\n", len(set.Programs), compiled, declared(read), len(outs), ran[outputs.RoleOutput], len(vals), ran[outputs.RoleValidator])
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

// kindNames are the names of the kinds s declares, and those of the kinds no unit reads: Secret
// and the kinds s declares withheld.
func kindNames(s *schema.Schemas) (declared, withheld map[string]bool) {
	declared, withheld = map[string]bool{}, map[string]bool{"Secret": true}
	for _, k := range s.Kinds() {
		declared[k.Kind] = true
		if k.Withheld {
			withheld[k.Kind] = true
		}
	}
	return declared, withheld
}

// checkPrograms refuses a definition of the role whose programs, by part, are not of the image's
// store, or whose inputs are kinds the image does not declare or withholds from its programs;
// outputs.Read and outputs.ReadValidators refused the rest already.
func checkPrograms(s *schema.Schemas, role outputs.Role, name string, inputs []string, programs map[string]string) []error {
	kinds, withheld := kindNames(s)
	var errs []error
	for _, part := range slices.Sorted(maps.Keys(programs)) {
		// InStore judges the program, the first word, alone: an argument such as /var/lib/ need
		// not be a clean path.
		if argv := strings.Fields(programs[part]); programs[part] != "" && (len(argv) == 0 || !command.InStore(argv[0])) {
			errs = append(errs, fmt.Errorf("%s %s: the %s %q is no program of the image's store", role, name, part, programs[part]))
		}
	}
	for _, kind := range inputs {
		switch {
		case !kinds[kind]:
			errs = append(errs, fmt.Errorf("%s %s: the image declares no kind %s", role, name, kind))
		case withheld[kind]:
			errs = append(errs, fmt.Errorf("%s %s: the image withholds %s documents from its programs", role, name, kind))
		}
	}
	return errs
}

// checkEffects refuses an effect whose outputs name no output, or whose kinds are not the image's
// or are withheld.
func checkEffects(s *schema.Schemas, outs map[string]outputs.Definition, effectDefs map[string]effects.Definition) []error {
	kinds, withheld := kindNames(s)
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(effectDefs)) {
		for _, o := range effectDefs[name].Outputs {
			if _, ok := outs[o]; !ok {
				errs = append(errs, fmt.Errorf("effect %s: outputs names %s, which is no output of the image; its outputs are %s", name, o, declared(outs)))
			}
		}
		for _, kind := range effectDefs[name].Kinds {
			switch {
			case withheld[kind]:
				errs = append(errs, fmt.Errorf("effect %s: an effect does not fire on %s documents", name, kind))
			case !kinds[kind]:
				errs = append(errs, fmt.Errorf("effect %s: the image declares no kind %s", name, kind))
			}
		}
	}
	return errs
}

// examples are the examples directories of the image's outputs and validators, by name, as
// examples.json holds them.
type examples struct {
	Outputs    map[string]string `json:"outputs"`
	Validators map[string]string `json:"validators"`
}

// checkExamples runs each output and each validator on each file of its examples directory, a
// config set of its own, as the node example, as a node would: an output renders it and passes its
// check, and a validator accepts it. It returns the number of examples run, by role. The build
// runs the programs as they are, and leaves the files' owner.
func checkExamples(s *schema.Schemas, outs map[string]outputs.Definition, vals map[string]outputs.Validator, ex examples) (map[outputs.Role]int, []error) {
	n := map[outputs.Role]int{}
	var errs []error
	for role, dirs := range map[outputs.Role]map[string]string{outputs.RoleOutput: ex.Outputs, outputs.RoleValidator: ex.Validators} {
		for _, name := range slices.Sorted(maps.Keys(dirs)) {
			_, isOutput := outs[name]
			_, isValidator := vals[name]
			if role == outputs.RoleOutput && !isOutput || role == outputs.RoleValidator && !isValidator {
				errs = append(errs, fmt.Errorf("%s %s: it has examples but is no %s", role, name, role))
				continue
			}
			files, err := exampleFiles(dirs[name])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s %s: its examples: %w", role, name, err))
				continue
			}
			for _, file := range files {
				if err := runExample(s, role, name, outs, vals, file); err != nil {
					errs = append(errs, fmt.Errorf("%s %s: example %s: %w", role, name, filepath.Base(file), err))
				}
				n[role]++
			}
		}
	}
	// The map's order is random; the errors are not.
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
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
// its defaults and redacted, as a node's renderers and validators read them.
func exampleDocuments(s *schema.Schemas, file string) (config.Redacted, error) {
	f, err := os.Open(file)
	if err != nil {
		return config.Redacted{}, err
	}
	docs, err := config.Parse(f)
	f.Close()
	if err != nil {
		return config.Redacted{}, err
	}
	for i, doc := range docs {
		if err := s.Validate(doc.Object); err != nil {
			return config.Redacted{}, err
		}
		filled, err := s.Default(doc.Object)
		if err != nil {
			return config.Redacted{}, err
		}
		docs[i].Object = filled
	}
	return config.Redact(docs), nil
}

// runExample runs the output or the validator name, as role says, on the example file.
func runExample(s *schema.Schemas, role outputs.Role, name string, outs map[string]outputs.Definition, vals map[string]outputs.Validator, file string) error {
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
	in := outputs.Input{Node: "example", Documents: docs, Schemas: s}
	var failures []outputs.Failure
	if role == outputs.RoleOutput {
		_, failures = r.Render(context.Background(), map[string]outputs.Definition{name: outs[name]}, in)
	} else if failures, err = r.Validate(context.Background(), map[string]outputs.Validator{name: vals[name]}, in); err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	// What the program wrote to standard error is in the build's log, above.
	if f := failures[0]; len(f.Refusals) > 0 {
		return errors.New(refusals.Message(f.Refusals))
	}
	return errors.New(strings.TrimSuffix(failures[0].Problem, "; see the node's journal"))
}
