package outputs

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/refusals"
)

// Validator is a validator as validators.json declares it.
type Validator struct {
	// Inputs are the kinds, by name, whose documents the program reads.
	Inputs []string `json:"inputs"`
	// Program is a program of the image's store with its arguments, to which chalkd appends
	// "validate" and the validator's name.
	Program string `json:"program"`
}

// ReadValidators reads the validators an image declares; a missing file declares none.
func ReadValidators(path string) (map[string]Validator, error) {
	return jsonfile.ReadDefs(path, string(RoleValidator), func(name string, v Validator) error { return v.check(name) })
}

// check refuses a definition of the validator name chalkd could not run: one checkDefinition
// refuses, or one that reads no kind.
func (v Validator) check(name string) error {
	if err := checkDefinition(name, v.Inputs, map[string]string{partProgram: v.Program}); err != nil {
		return err
	}
	if len(v.Inputs) == 0 {
		return errors.New("it reads no kind")
	}
	return nil
}

// Validate runs every validator of defs, in the order of their names, on the documents of in, as
// Render runs a renderer, below Root/.validating, and returns the failures of those that refused
// the config or did not run to their end. Each program gets an empty directory of its own as
// $OUT, which it cannot write, and opens no netlink socket. An error is chalkd's own, such as one
// preparing that directory, and no validator's.
func (r Renderer) Validate(ctx context.Context, defs map[string]Validator, in Input) ([]Failure, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	dir := filepath.Join(r.Root, ".validating")
	// What a run that chalkd did not finish left there goes first.
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var failures []Failure
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		v := defs[name]
		out, err := scratch(dir, name)
		if err != nil {
			return nil, fmt.Errorf("validator %s: %w", name, err)
		}
		stdin, err := in.stdin(v.Inputs)
		if err != nil {
			return nil, fmt.Errorf("validator %s: %w", name, err)
		}
		argv := append(strings.Fields(v.Program), "validate", name)
		err = r.run(ctx, RoleValidator, name, partProgram, Program{Argv: argv, Stdin: stdin, Out: out, Report: true})
		os.RemoveAll(out)
		if err != nil {
			failures = append(failures, failure(RoleValidator, name, err))
		}
	}
	return failures, nil
}

// A ValidateFunc is a validator linked into a program, which reads what a validator's program
// reads on standard input and returns its refusals. An error is a failure of the validator itself.
type ValidateFunc func(stdin []byte) ([]refusals.Refusal, error)

// LinkedValidator is a validator a program links: its name, as an image declares the validator,
// the kinds whose documents it reads, and its ValidateFunc.
type LinkedValidator struct {
	Name     string
	Inputs   []string
	Validate ValidateFunc
}

// RunLinked runs every linked validator, in the order given, on the documents of in, in-process,
// as Validate runs a validator's program: on the same JSON, with its report held to the same
// format, and returns the failures of those that refused the config or failed.
func RunLinked(linked []LinkedValidator, in Input) []Failure {
	var failures []Failure
	for _, l := range linked {
		if err := runLinked(l, in); err != nil {
			failures = append(failures, failure(RoleValidator, l.Name, err))
		}
	}
	return failures
}

// runLinked runs l on in, and parses its refusals as Validate parses a program's report, so a
// linked validator refuses nothing a program could not print. It first replaces the runes of each
// message that refusals.Unprintable reports by spaces, as chalkos-render does before it prints a
// report, so a message that quotes one stays a refusal.
func runLinked(l LinkedValidator, in Input) error {
	stdin, err := in.stdin(l.Inputs)
	if err != nil {
		return err
	}
	rs, err := l.Validate(stdin)
	if err != nil {
		return fmt.Errorf("its %s failed: %w", partProgram, err)
	}
	for i := range rs {
		rs[i].Message = refusals.Printable(rs[i].Message)
	}
	report, err := refusals.Encode(rs)
	if err != nil {
		return err
	}
	if rs, err = refusals.Parse(report); err != nil {
		return fmt.Errorf("its %s's report: %w", partProgram, err)
	}
	if len(rs) > 0 {
		return &refused{partProgram, rs}
	}
	return nil
}
