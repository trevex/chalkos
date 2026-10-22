// Command chalkos-rules checks an image when it is built: every change rule its schemas declare
// and every rule of its effects compiles, within the cost limit, and reads only facts the image
// declares, each a file or a program of the image's store; and every effect acts on units the
// image defines.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

const usage = `usage: chalkos-rules check IMAGE

Checks the files of the directory IMAGE, as an image's build lays them out: schemas/, the
directory of its schemas; facts.json, effects.json, the image's own; and units/, its systemd
units. Every rule of the schemas and of the effects compiles and reads only facts facts.json
declares, and every unit an effect restarts or reloads is one of units/.`

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
	effects, err := nodeconfig.ReadEffects(filepath.Join(image, "effects.json"))
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range sortedNames(defs) {
		if err := checkFact(name, defs[name]); err != nil {
			errs = append(errs, err)
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
	for _, name := range sortedNames(effects) {
		e := effects[name]
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
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(out, "%d rules and %d effect rules compile, reading the facts %s\n", len(set.Programs), compiled, declared(read))
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

// checkFact refuses a fact that is neither a file nor a program of the image's store with
// arguments, that takes inputs a file does not, or keeps or hashes fields a command does not.
func checkFact(name string, d facts.Definition) error {
	switch {
	case (d.File == "") == (d.Command == ""):
		return fmt.Errorf("fact %s: it has either a file or a command", name)
	case d.Command != "" && (len(d.Fields) > 0 || d.HashOthers):
		return fmt.Errorf("fact %s: only a file keeps fields or hashes them", name)
	case d.File != "" && len(d.Inputs) > 0:
		return fmt.Errorf("fact %s: only a command takes inputs", name)
	case d.Command != "" && !strings.HasPrefix(d.Command, "/nix/store/"):
		return fmt.Errorf("fact %s: the command %q is no program of the image's store", name, d.Command)
	}
	for _, in := range d.Inputs {
		if in != facts.FallbackSecret {
			return fmt.Errorf("fact %s: there is no input %q; the inputs are %s", name, in, facts.FallbackSecret)
		}
	}
	return nil
}

func declared[V any](m map[string]V) string {
	names := sortedNames(m)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
