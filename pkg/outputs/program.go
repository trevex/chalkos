package outputs

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
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/constants"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/schema"
)

// Input is what renderers and validators read: a node's redacted config set with its defaults,
// the schemas that tell its secretRef fields, and the node's name. A hidden value's hash stands in
// for the value, so the states still change when a secret does.
type Input struct {
	Node      string
	Documents config.Redacted
	Schemas   *schema.Schemas
}

// stdin is what a renderer, a check or a validator of the kinds reads on standard input: the
// node's name and the documents of each kind, sorted by name, as rules see them, with their schema
// defaults, and an empty list for a kind without documents. No program sees a Secret's values.
func (in Input) stdin(kinds []string) ([]byte, error) {
	docs := map[string][]any{}
	for _, kind := range kinds {
		docs[kind] = []any{}
	}
	for _, doc := range in.of(kinds) {
		docs[doc.Kind] = append(docs[doc.Kind], doc.Object)
	}
	return json.Marshal(map[string]any{
		"node":      map[string]any{"name": in.Node},
		"documents": docs,
	})
}

// of are the documents of the kinds, sorted by kind and name, without the Secrets.
func (in Input) of(kinds []string) []config.Document {
	var out []config.Document
	for _, doc := range in.Documents.Documents() {
		if !doc.IsSecret() && slices.Contains(kinds, doc.Kind) {
			out = append(out, doc)
		}
	}
	slices.SortStableFunc(out, func(a, b config.Document) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// Role is what a definition of the image is to chalkd: an output or a validator.
type Role string

const (
	RoleOutput    Role = "output"
	RoleValidator Role = "validator"
)

// A Failure is an output or validator that refused the config or did not run to its end. Standard
// error goes to the journal alone, since it can echo the documents the program read.
type Failure struct {
	Name     string
	Role     Role
	Problem  string
	Refusals []refusals.Refusal
}

func (f Failure) Error() string { return string(f.Role) + " " + f.Name + ": " + f.Problem }

// OffNode is the failure as Error says it without the pointer to the journal, for a caller that
// runs the programs elsewhere and logs what they write to standard error itself, as an image's
// build does.
func (f Failure) OffNode() string {
	s := f.Error()
	if i := strings.LastIndex(s, logsHint); i >= 0 {
		return s[:i]
	}
	return strings.TrimSuffix(s, journalHint)
}

// The endings of the problem of a program that failed, which wrote what says why to standard
// error: in the sandbox, logsHint, the command that shows its unit's journal; else journalHint.
const (
	logsHint    = "; see chalkctl logs "
	journalHint = "; see the node's journal"
)

// seeJournal is the ending of the problem of a program of the node that failed in unit, or
// outside a unit of its own where unit is empty.
func seeJournal(node, unit string) string {
	if unit == "" {
		return journalHint
	}
	return logsHint + cmp.Or(node, "<node>") + " --unit=" + unit
}

// Message is the message a config the failures refuse is refused with: the refusals first, each
// once, since programs that share a check can report the same one, then the other failures, all
// joined by "; ".
func Message(failures []Failure) string {
	var refused []refusals.Refusal
	var msgs []string
	for _, f := range failures {
		if len(f.Refusals) == 0 {
			msgs = append(msgs, f.Error())
		}
		refused = append(refused, f.Refusals...)
	}
	if len(refused) > 0 {
		msgs = append([]string{refusals.Message(refused)}, msgs...)
	}
	return strings.Join(msgs, "; ")
}

// Join is the failures as one error, in the order given.
func Join(failures []Failure) error {
	errs := make([]error, len(failures))
	for i, f := range failures {
		errs[i] = f
	}
	return errors.Join(errs...)
}

// failure is the failure of the role's definition name for err, with the refusals of a *refused.
func failure(role Role, name string, err error) Failure {
	f := Failure{Name: name, Role: role, Problem: err.Error()}
	if r, ok := errors.AsType[*refused](err); ok {
		f.Refusals = r.refusals
	}
	return f
}

// refused is a config a program refused, with the refusals it reported; part names the program.
type refused struct {
	part     string
	refusals []refusals.Refusal
}

func (r *refused) Error() string {
	lines := make([]string, len(r.refusals))
	for i, x := range r.refusals {
		lines[i] = x.String()
	}
	return "its " + r.part + " refused the config: " + strings.Join(lines, "; ")
}

// The parts of a definition, its programs as a failure names them.
const (
	partRenderer = "renderer"
	partCheck    = "check"
	partProgram  = "program"
)

// run runs p, the part of the role's definition name, with the runner and timeout of r, on the
// node node. A
// renderer's or validator's refusals on standard output make a *refused whatever its exit status.
// One that exits 0 and prints anything else fails for what it printed. A check's standard output
// is discarded, because a service's own check can echo the files. A failed program's standard
// error, or the unit whose journal holds it, is logged.
func (r Renderer) run(ctx context.Context, node string, role Role, name, part string, p Program) error {
	p.Timeout = cmp.Or(r.Timeout, DefaultTimeout)
	printed, err := r.Run.Run(ctx, p)
	failed, _ := errors.AsType[*ProgramError](err)
	if failed != nil {
		if failed.Unit != "" {
			log.Printf("%s %s: its %s %s failed (%s); what it wrote to standard error is in the journal of %s", role, name, part, p.Argv[0], failed.Status, failed.Unit)
		} else {
			log.Printf("%s %s: its %s %s failed (%s): %q", role, name, part, p.Argv[0], failed.Status, failed.Stderr)
		}
	}
	if p.Report && (err == nil || failed != nil) {
		rs, perr := refusals.Parse(printed)
		switch {
		case perr != nil && failed != nil:
			// A failed program's failure says more than what it printed instead of a report.
			log.Printf("%s %s: its %s %s printed no report of refusals: %v", role, name, part, p.Argv[0], perr)
		case perr != nil:
			return fmt.Errorf("its %s's standard output: %w", part, perr)
		case len(rs) > 0:
			return &refused{part, rs}
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("its %s did not finish within %s", part, p.Timeout)
	case failed != nil && part == partCheck:
		return fmt.Errorf("its check refused the rendered files (%s)%s", failed.Status, seeJournal(node, failed.Unit))
	case failed != nil:
		return fmt.Errorf("its %s failed (%s)%s", part, failed.Status, seeJournal(node, failed.Unit))
	}
	return fmt.Errorf("its %s: %w", part, err)
}

// scratch makes a directory of its own for a program of the definition name below parent, which
// it makes first, readable by the renderers' user.
func scratch(parent, name string) (string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, name+".")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// plainName is the name of an output or a validator, which names it in refusals, in its directories
// and in a validator's argv.
var plainName = constants.PlainName

// checkDefinition refuses a definition of the image chalkd could not run: a name that is not
// plain, an input of Secret, or a program that is no absolute path. The image's build checks what
// needs the store: that the programs are of it and the kinds are declared.
func checkDefinition(n string, inputs []string, programs map[string][]string) error {
	if !plainName.MatchString(n) {
		return errors.New("the name is not lower-case letters, digits and dashes")
	}
	if slices.Contains(inputs, "Secret") {
		return errors.New("it reads Secret documents, whose values no program reads")
	}
	for _, part := range slices.Sorted(maps.Keys(programs)) {
		if argv := programs[part]; len(argv) == 0 || !filepath.IsAbs(argv[0]) {
			return fmt.Errorf("the %s %q names no program by its absolute path", part, strings.Join(argv, " "))
		}
	}
	return nil
}
