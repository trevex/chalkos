// Command chalkos-rules checks an image when it is built, and runs its tests.
//
// The check: every change rule its schemas declare and every rule of its effects compiles, within
// the cost limit, and reads only facts the image declares, each a file or a program of the image's
// store; every effect acts on units the image defines, outputs it declares and kinds its schemas
// declare; every output and validator is a program of the image's store that reads kinds the
// image gives its programs; and every diagnostic is one chalkd can run.
//
// The tests: each config of chalkos.tests goes through the checks chalkd runs for a delivery, and
// what it got, the outputs' files, the refusals and the effects due, is compared with what the
// test expects.
package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/command"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
)

const usage = `usage: chalkos-rules check IMAGE
       chalkos-rules test IMAGE TESTS OUT

check checks the files of the directory IMAGE, as an image's build lays them out: schemas/, the
directory of its schemas; facts.json, effects.json, outputs.json, validators.json and
diagnostics.json, the image's own; and units/, its systemd units. Every rule of the schemas and of
the effects compiles and reads only facts facts.json declares, every unit an effect restarts or
reloads is one of units/, every effect's outputs and kinds name an output or a kind that exists,
every output and validator is a program of the image's store and reads kinds the schemas declare,
and every diagnostic is one chalkd can run.

test runs the tests of TESTS, chalkos.tests as the image's build writes them, against the image
IMAGE, and writes what each got to OUT, which should be new or empty, as the files of an earlier
run stay: OUT/<test>/outputs/<output>/, each output's files with their store paths' hashes
replaced by eeee...; OUT/<test>/refusals.json, the refusals, [] for an accepted config;
OUT/<test>/effects.json, the effects due, sorted by name; OUT/<test>/warnings, the effect rules
that could not be evaluated, when there are any; OUT/summary.txt, a PASS or FAIL line per test
and a WARN line per warning, which fails no test; OUT/failures, the FAIL lines alone; and
OUT/goldens.tsv, a line per output that differs from its golden files: the test, the output, the
golden files and the rendered files below OUT. A test's refusals are always compared, none
expected when it gives none; its effects only when it gives them; and a fact it gives
that the image does not declare fails it. A test whose config cannot be read, whose before is
refused or whose check, or that of its before, fails gets its FAIL lines alone, with no files
below OUT/<test>. It exits 0 whatever the tests got.`

func main() {
	var err error
	switch {
	case len(os.Args) == 3 && os.Args[1] == "check":
		err = checkImage(os.Args[2], os.Stdout)
	case len(os.Args) == 5 && os.Args[1] == "test":
		err = runTests(os.Args[2], os.Args[3], os.Args[4])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chalkos-rules: "+err.Error())
		os.Exit(1)
	}
}

// checkImage checks the image of the directory image, as check of the usage says, and writes what
// it checked to out.
func checkImage(image string, out io.Writer) error {
	c, err := contract.Load(image)
	if err != nil {
		return err
	}
	s, defs := c.Schemas, c.Facts
	var errs []error
	read := map[string]bool{}
	reads := func(where, rule string, names []string) {
		for _, f := range names {
			if _, ok := defs[f]; !ok {
				errs = append(errs, fmt.Errorf("%s: rule %q reads facts.%s, which the image does not declare; it declares %s", where, rule, f, declared(defs)))
			}
			read[f] = true
		}
	}
	set, err := c.Rules()
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, p := range set.Programs {
			reads(p.Where(), p.Rule.Rule, p.Facts)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Effects)) {
		e := c.Effects[name]
		for _, unit := range slices.Concat(e.Restart, e.Reload) {
			if err := checkUnit(image, unit); err != nil {
				errs = append(errs, fmt.Errorf("effect %s: %w", name, err))
			}
		}
	}
	effectRules, err := c.EffectRules()
	if err != nil {
		errs = append(errs, err)
	}
	for _, p := range effectRules {
		reads(p.Where(), p.When, p.Facts)
	}
	kinds := kindNamesOf(s)
	outs, vals := c.Outputs, c.Validators
	for _, name := range slices.Sorted(maps.Keys(outs)) {
		errs = append(errs, checkPrograms(kinds, outputs.RoleOutput, name, outs[name].Inputs, map[string]string{"renderer": outs[name].Render, "check": outs[name].Check})...)
	}
	for _, name := range slices.Sorted(maps.Keys(vals)) {
		errs = append(errs, checkPrograms(kinds, outputs.RoleValidator, name, vals[name].Inputs, map[string]string{"program": vals[name].Program})...)
	}
	errs = append(errs, checkEffects(kinds, outs, c.Effects)...)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(out, "%d rules and %d effect rules compile, reading the facts %s\n", len(set.Programs), len(effectRules), declared(read))
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

// kindNames are the names of the kinds an image's schemas declare, and those of the kinds no unit
// reads: Secret and the kinds the schemas declare withheld.
type kindNames struct {
	declared, withheld map[string]bool
}

func kindNamesOf(s *schema.Schemas) kindNames {
	k := kindNames{declared: map[string]bool{}, withheld: map[string]bool{"Secret": true}}
	for _, kind := range s.Kinds() {
		k.declared[kind.Kind] = true
		if kind.Withheld {
			k.withheld[kind.Kind] = true
		}
	}
	return k
}

// checkPrograms refuses a definition of the role whose programs, by part, are not of the image's
// store, or whose inputs are kinds the image does not declare or withholds from its programs;
// contract.Load refused the rest already.
func checkPrograms(kinds kindNames, role outputs.Role, name string, inputs []string, programs map[string]string) []error {
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
		case !kinds.declared[kind]:
			errs = append(errs, fmt.Errorf("%s %s: the image declares no kind %s", role, name, kind))
		case kinds.withheld[kind]:
			errs = append(errs, fmt.Errorf("%s %s: the image withholds %s documents from its programs", role, name, kind))
		}
	}
	return errs
}

// checkEffects refuses an effect whose outputs name no output, or whose kinds are not the image's
// or are withheld.
func checkEffects(kinds kindNames, outs map[string]outputs.Definition, effectDefs map[string]effects.Definition) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(effectDefs)) {
		for _, o := range effectDefs[name].Outputs {
			if _, ok := outs[o]; !ok {
				errs = append(errs, fmt.Errorf("effect %s: outputs names %s, which is no output of the image; its outputs are %s", name, o, declared(outs)))
			}
		}
		for _, kind := range effectDefs[name].Kinds {
			switch {
			case kinds.withheld[kind]:
				errs = append(errs, fmt.Errorf("effect %s: an effect does not fire on %s documents", name, kind))
			case !kinds.declared[kind]:
				errs = append(errs, fmt.Errorf("effect %s: the image declares no kind %s", name, kind))
			}
		}
	}
	return errs
}
