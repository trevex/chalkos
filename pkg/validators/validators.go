// Package validators runs an image's validators: programs that check a node's documents for what
// no schema or change rule of the image sees, such as what several documents mean together, or
// what takes a parser or a tool to tell. Each validator of /etc/chalkos/validators.json names the
// kinds it reads and its program, a program of the image's store, which chalkd runs as
//
//	<program> validate <name>
//
// in the sandbox renderers run in. The program reads the documents of its input kinds on standard
// input as one JSON array, sorted by kind and name, with their schema defaults, without the
// Secrets and with every other secret value hashed, and prints a report of refusals on standard
// output, as pkg/refusals describes it. Exit status 0 means that it ran: the config is valid when
// the report holds no refusal. Any other status is a failure of the validator itself, which
// refuses the config too, as a renderer that fails does; what it wrote to standard error goes to
// the node's journal alone.
//
// chalkd runs the validators after the change rules and before the renderers, in a delivery, its
// dry run, an install in place and the image's check of its own config, and at boot before it
// renders the outputs, as an installer writes a slot without running them. chalkctl runs the
// validators it links, Funcs that read the same JSON, in-process when it checks a node's config.
package validators

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config/withheld"
	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
)

// DefaultPath is where an image declares its validators.
const DefaultPath = "/etc/chalkos/validators.json"

// Definition is a validator as validators.json declares it.
type Definition struct {
	// Inputs are the kinds, by name, whose documents the program reads.
	Inputs []string `json:"inputs"`
	// Program is a program of the image's store with its arguments, to which chalkd appends
	// "validate" and the validator's name.
	Program string `json:"program"`
}

// name is a validator's name, which names it in refusals and in its argv: lower-case letters,
// digits and dashes, as an output's.
var name = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Read reads the validators an image declares; a missing file declares none.
func Read(path string) (map[string]Definition, error) {
	defs := map[string]Definition{}
	if err := jsonfile.Read(path, &defs); err != nil {
		return nil, err
	}
	var errs []error
	for _, n := range slices.Sorted(maps.Keys(defs)) {
		if err := defs[n].check(n); err != nil {
			errs = append(errs, fmt.Errorf("%s: validator %s: %w", path, n, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return defs, nil
}

// check refuses a definition of the validator n chalkd could not run: a name that is not plain, a
// program that is no absolute path, or inputs that are none or name a kind no program reads. The
// image's build checks that the program is of its store and that the image declares the kinds.
func (d Definition) check(n string) error {
	if !name.MatchString(n) {
		return errors.New("a validator's name is lower-case letters, digits and dashes")
	}
	if argv := strings.Fields(d.Program); len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return fmt.Errorf("the program %q names no program by its absolute path", d.Program)
	}
	if len(d.Inputs) == 0 {
		return errors.New("it reads no kind")
	}
	for _, kind := range d.Inputs {
		if withheld.Kind(kind) {
			return fmt.Errorf("a validator does not read %s documents", kind)
		}
	}
	return nil
}

// Stdin is what a validator of the kinds reads: the documents of in of those kinds as one JSON
// array, as outputs.Input.Objects gives them; an empty array for none.
func Stdin(in outputs.Input, kinds []string) ([]byte, error) {
	return json.Marshal(in.Objects(kinds))
}

// A Failure is a validator that did not run to its end, which refuses the config as a refusal
// does. Its problem says how it failed, never what it wrote to standard error, which can echo the
// documents and goes to the journal alone.
type Failure struct {
	Validator, Problem string
}

func (f Failure) Error() string { return "validator " + f.Validator + " " + f.Problem }

// Refusal is the message a config the validators refused is refused with, as chalkd and check.Run
// say it: the refusals as refusals.Message reports them, each once, then each failure as its
// Error says it, all joined by "; ".
func Refusal(rs []refusals.Refusal, failures []Failure) string {
	var msgs []string
	if len(rs) > 0 {
		msgs = append(msgs, refusals.Message(rs))
	}
	for _, f := range failures {
		msgs = append(msgs, f.Error())
	}
	return strings.Join(msgs, "; ")
}

// Run runs every validator of defs, in the order of their names, on the documents of in, with the
// runner and the timeout of r, below r.Root/.validating, and returns the refusals they reported
// and the failures of those that did not run to their end. Each program gets an empty directory
// of its own as $OUT, which it cannot write, and opens no netlink socket. An error is chalkd's
// own, such as one preparing that directory, and no validator's.
func Run(ctx context.Context, r outputs.Renderer, defs map[string]Definition, in outputs.Input) ([]refusals.Refusal, []Failure, error) {
	if len(defs) == 0 {
		return nil, nil, nil
	}
	var rs []refusals.Refusal
	var failures []Failure
	dir := filepath.Join(r.Root, ".validating")
	// What a run that chalkd did not finish left there goes first.
	if err := os.RemoveAll(dir); err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	for _, n := range slices.Sorted(maps.Keys(defs)) {
		got, err := run(ctx, r, n, defs[n], in, dir)
		if f, ok := errors.AsType[*Failure](err); ok {
			failures = append(failures, *f)
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("validator %s: %w", n, err)
		}
		rs = append(rs, got...)
	}
	return rs, failures, nil
}

// run runs the validator n of d below dir, logging what a failed program wrote to standard error,
// or the unit whose journal holds it. A validator that does not run to its end, or prints no
// report of refusals, is a *Failure; any other error is chalkd's own.
func run(ctx context.Context, r outputs.Renderer, n string, d Definition, in outputs.Input, dir string) ([]refusals.Refusal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out, err := os.MkdirTemp(dir, n+".")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out)
	if err := os.Chmod(out, 0o755); err != nil {
		return nil, err
	}
	stdin, err := Stdin(in, d.Inputs)
	if err != nil {
		return nil, err
	}
	timeout := cmp.Or(r.Timeout, outputs.DefaultTimeout)
	argv := append(strings.Fields(d.Program), "validate", n)
	printed, err := r.Run.Run(ctx, outputs.Program{Argv: argv, Stdin: stdin, Out: out, Report: true, Timeout: timeout})
	var failed *outputs.ProgramError
	switch {
	case errors.As(err, &failed):
		if failed.Unit != "" {
			log.Printf("validator %s: its program %s failed (%s); what it wrote to standard error is in the journal of %s", n, argv[0], failed.Status, failed.Unit)
		} else {
			log.Printf("validator %s: its program %s failed (%s): %q", n, argv[0], failed.Status, failed.Stderr)
		}
		return nil, &Failure{Validator: n, Problem: fmt.Sprintf("failed (%s); see the node's journal", failed.Status)}
	case errors.Is(err, context.DeadlineExceeded):
		return nil, &Failure{Validator: n, Problem: fmt.Sprintf("did not finish within %s", timeout)}
	case err != nil:
		return nil, &Failure{Validator: n, Problem: fmt.Sprintf("failed: %v", err)}
	}
	rs, err := refusals.Parse(printed)
	if err != nil {
		return nil, &Failure{Validator: n, Problem: fmt.Sprintf("failed: its standard output: %v", err)}
	}
	return rs, nil
}

// A Func is a validator linked into a program, which reads what a validator's program reads on
// standard input, the JSON array of the documents of its input kinds, and returns its refusals.
// An error is a failure of the validator itself.
type Func func(documents []byte) ([]refusals.Refusal, error)

// Linked is a validator a program links: its name, as an image declares the validator, the kinds
// whose documents it reads, and its Func.
type Linked struct {
	Name     string
	Inputs   []string
	Validate Func
}

// RunLinked runs every linked validator, in the order given, on the documents of in, in-process,
// as Run runs a validator's program: on the same JSON, with its report held to the same format.
func RunLinked(linked []Linked, in outputs.Input) ([]refusals.Refusal, []Failure) {
	var rs []refusals.Refusal
	var failures []Failure
	for _, l := range linked {
		got, err := runLinked(l, in)
		if err != nil {
			failures = append(failures, Failure{Validator: l.Name, Problem: "failed: " + err.Error()})
			continue
		}
		rs = append(rs, got...)
	}
	return rs, failures
}

// runLinked runs l on in, and parses its refusals as Run parses a program's report, so a linked
// validator refuses nothing a program could not print. It first replaces the runes of each message
// that refusals.Unprintable reports by spaces, as chalkos-render does before it prints a report,
// so a message that quotes one stays a refusal.
func runLinked(l Linked, in outputs.Input) ([]refusals.Refusal, error) {
	stdin, err := Stdin(in, l.Inputs)
	if err != nil {
		return nil, err
	}
	rs, err := l.Validate(stdin)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		rs[i].Message = refusals.Printable(rs[i].Message)
	}
	report, err := refusals.Encode(rs)
	if err != nil {
		return nil, err
	}
	if rs, err = refusals.Parse(report); err != nil {
		return nil, fmt.Errorf("its report: %w", err)
	}
	return rs, nil
}
