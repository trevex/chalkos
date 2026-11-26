// Command chalkos-rules checks an image when it is built, its rules compiling within the cost
// limit, and runs its tests; see usage.
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
	"github.com/trevex/chalkos/pkg/outputs"
)

const usage = `usage: chalkos-rules check IMAGE
       chalkos-rules test IMAGE TESTS OUT

check checks the files of the directory IMAGE, as an image's build lays them out: schemas/, the
directory of its schemas; facts.json, effects.json, outputs.json, validators.json and
diagnostics.json, the image's own; and units/, its systemd units. Every rule of the schemas and of
the effects compiles and reads only facts facts.json declares, every unit an effect restarts or
reloads is one of units/, the programs of every output and validator are of the image's store,
and every definition, diagnostics among them, is one chalkd can run. A rule without message or
messageExpression gets a warning line.

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
	defs := c.Facts
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
			if p.Message == "" && p.MessageExpression == "" {
				fmt.Fprintf(out, "warning: %s: rule %q has neither message nor messageExpression, so its refusals say only which rule refused; give it a message that says why and what to do instead\n", p.Where(), p.Rule.Rule)
			}
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
	outs, vals := c.Outputs, c.Validators
	for _, name := range slices.Sorted(maps.Keys(outs)) {
		errs = append(errs, checkPrograms(outputs.RoleOutput, name, map[string][]string{"renderer": outs[name].Render, "check": outs[name].Check})...)
	}
	for _, name := range slices.Sorted(maps.Keys(vals)) {
		errs = append(errs, checkPrograms(outputs.RoleValidator, name, map[string][]string{"program": vals[name].Program})...)
	}
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

// checkPrograms refuses a definition of the role whose programs, by part, are not of the image's
// store; contract.Load refused the rest already.
func checkPrograms(role outputs.Role, name string, programs map[string][]string) []error {
	var errs []error
	for _, part := range slices.Sorted(maps.Keys(programs)) {
		// InStore judges the program alone: an argument such as /var/lib/ need not be a clean path.
		if argv := programs[part]; len(argv) > 0 && !command.InStore(argv[0]) {
			errs = append(errs, fmt.Errorf("%s %s: the %s %q is no program of the image's store", role, name, part, strings.Join(argv, " ")))
		}
	}
	return errs
}
