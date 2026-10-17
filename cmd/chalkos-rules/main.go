// Command chalkos-rules checks the rules of an image when it is built: every change rule its
// schemas declare and every restart and reload rule of its consumers compiles, within the cost
// limit, and reads only facts the image declares, each a file or a program of the image's store.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

const usage = `usage: chalkos-rules check SCHEMAS FACTS CONSUMERS

Compiles every rule of the schemas directory SCHEMAS and every restart and reload rule of
CONSUMERS, an image's consumers.json, and checks that each reads only the facts FACTS, an image's
facts.json, declares.`

func main() {
	if len(os.Args) != 5 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := check(os.Args[2], os.Args[3], os.Args[4], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "chalkos-rules: "+err.Error())
		os.Exit(1)
	}
}

func check(schemasDir, factsFile, consumersFile string, out io.Writer) error {
	s, err := schema.LoadSchemas(schemasDir)
	if err != nil {
		return err
	}
	defs, err := facts.Read(factsFile)
	if err != nil {
		return err
	}
	consumers, err := nodeconfig.ReadConsumers(consumersFile)
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
	effects := 0
	for _, unit := range sortedNames(consumers) {
		c := consumers[unit]
		for _, r := range []struct{ action, when string }{{rules.Restart, c.Restart.When}, {rules.Reload, c.Reload.When}} {
			e, err := rules.CompileEffect(unit, r.action, r.when)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			reads(e.Where(), e.When, e.Facts)
			effects++
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(out, "%d rules and %d consumer rules compile, reading the facts %s\n", len(set.Programs), effects, declared(read))
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
