// Command chalkos-rules checks the change rules of an image when it is built: every rule its
// schemas declare compiles, within the cost limit, and reads only facts the image declares, each
// a file or a program of the image's store.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

const usage = `usage: chalkos-rules check SCHEMAS FACTS

Compiles every rule of the schemas directory SCHEMAS and checks that each reads only the facts
FACTS, an image's facts.json, declares.`

func main() {
	if len(os.Args) != 4 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := check(os.Args[2], os.Args[3], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "chalkos-rules: "+err.Error())
		os.Exit(1)
	}
}

func check(schemasDir, factsFile string, out io.Writer) error {
	s, err := schema.LoadSchemas(schemasDir)
	if err != nil {
		return err
	}
	defs, err := facts.Read(factsFile)
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
	if set != nil {
		for _, p := range set.Programs {
			for _, f := range p.Facts {
				if _, ok := defs[f]; !ok {
					errs = append(errs, fmt.Errorf("%s: rule %q reads facts.%s, which the image does not declare; it declares %s", p.Where(), p.Rule.Rule, f, declared(defs)))
				}
				read[f] = true
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(out, "%d rules compile, reading the facts %s\n", len(set.Programs), declared(read))
	return nil
}

// checkFact refuses a fact that is neither a file nor a program of the image's store with
// arguments, or that takes inputs a command does not.
func checkFact(name string, d facts.Definition) error {
	switch {
	case (d.File == "") == (d.Command == ""):
		return fmt.Errorf("fact %s: it has either a file or a command", name)
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
