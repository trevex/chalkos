package outputs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/schema"
)

// Input is what outputs render from: a node's config set, with its defaults, the schemas that
// tell its secretRef fields, and the node's name. The set is redacted, so no program that reads
// it sees a secret value, and the states take a hidden value, the hash of the value, as it is.
type Input struct {
	Node      string
	Documents config.Redacted
	Schemas   *schema.Schemas
}

// stdin is what a renderer reads: the node's name and the documents of each input kind, sorted by
// name, as rules see them, with their schema defaults. The renderer never sees a Secret's values.
func (d Definition) stdin(in Input) ([]byte, error) {
	docs := map[string][]any{}
	for _, kind := range d.Inputs {
		docs[kind] = []any{}
	}
	for _, doc := range in.of(d.Inputs) {
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

// Objects are the documents of the kinds as the programs that read them get them, renderers and
// validators: sorted by kind and name, with their schema defaults and their secret values hashed,
// and without the Secrets, whose values no program sees.
func (in Input) Objects(kinds []string) []map[string]any {
	docs := in.of(kinds)
	out := make([]map[string]any, len(docs))
	for i, doc := range docs {
		out[i] = doc.Object
	}
	return out
}

// A Failure is an output that could not be rendered, or whose check refused it. Its problem names
// what failed and its exit status, or, for a renderer that refused the config, the refusals it
// reported, which Refusals holds too. What a program wrote to standard error goes to the journal
// alone, since it can echo the documents it read.
type Failure struct {
	Output, Problem string
	Refusals        []refusals.Refusal
}

func (f Failure) Error() string { return "output " + f.Output + ": " + f.Problem }

// Refusal is the message a delivery whose outputs failed is refused with, as chalkd and check.Run
// say it. The renderers' refusals come first, as refusals.Message reports them: each once, since
// the outputs that share a check refuse a config with the same refusal. Each other failure
// follows as its Error says it, in the order given, all joined by "; ". Join gives the failures
// as one error instead.
func Refusal(failures []Failure) string {
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

// refused is a config an output's renderer refused, with the refusals it reported.
type refused struct{ refusals []refusals.Refusal }

func (r *refused) Error() string {
	lines := make([]string, len(r.refusals))
	for i, x := range r.refusals {
		lines[i] = x.String()
	}
	return "its renderer refused the config: " + strings.Join(lines, "; ")
}

// Renderer renders outputs below Root.
type Renderer struct {
	Root string
	Run  Runner
	// Timeout bounds each program; zero is DefaultTimeout.
	Timeout time.Duration
	// KeepOwner leaves the files the renderer's, as the image's build does, which has none of the
	// image's users.
	KeepOwner bool
}

// Default renders on the node, in the sandbox.
func Default() Renderer {
	return Renderer{Root: DefaultRoot, Run: Sandbox{}}
}

// Render renders and checks every output of defs from in, each in a directory of its own below
// Root/.staging, and returns those that passed and the failures of the others. It changes nothing
// a service reads; what an earlier render left staged is removed first.
func (r Renderer) Render(ctx context.Context, defs map[string]Definition, in Input) (*Staged, []Failure) {
	s := &Staged{root: r.Root, defs: defs, staging: map[string]string{}, states: map[string]State{}}
	if len(defs) == 0 {
		return s, nil
	}
	staging := filepath.Join(r.Root, ".staging")
	var failures []Failure
	fail := func(name string, err error) {
		f := Failure{Output: name, Problem: err.Error()}
		if r, ok := err.(*refused); ok {
			f.Refusals = r.refusals
		}
		failures = append(failures, f)
	}
	if err := os.RemoveAll(staging); err != nil {
		for _, name := range slices.Sorted(maps.Keys(defs)) {
			fail(name, err)
		}
		return s, failures
	}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		dir, state, err := r.render(ctx, name, defs[name], in, staging)
		if err != nil {
			if dir != "" {
				os.RemoveAll(dir)
			}
			fail(name, err)
			continue
		}
		s.staging[name], s.states[name] = dir, state
	}
	return s, failures
}

func (r Renderer) render(ctx context.Context, name string, d Definition, in Input, staging string) (string, State, error) {
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", State{}, err
	}
	dir, err := os.MkdirTemp(staging, name+".")
	if err != nil {
		return "", State{}, err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return dir, State{}, err
	}
	stdin, err := d.stdin(in)
	if err != nil {
		return dir, State{}, err
	}
	if err := r.run(ctx, name, roleRenderer, d.Render, stdin, dir); err != nil {
		return dir, State{}, err
	}
	if err := checkFiles(dir); err != nil {
		return dir, State{}, err
	}
	// The check reads the files as the renderer wrote them, which the output's owner and mode may
	// keep from the sandbox's user.
	if d.Check != "" {
		if err := r.run(ctx, name, roleCheck, d.Check, stdin, dir); err != nil {
			return dir, State{}, err
		}
	}
	files, err := hashDir(dir)
	if err != nil {
		return dir, State{}, err
	}
	if err := own(dir, d, r.KeepOwner); err != nil {
		return dir, State{}, err
	}
	return dir, State{Files: files, Secrets: d.secrets(in)}, nil
}

// run runs a renderer or a check of the output name, the renderer with out writable, logging what
// a failed program wrote to standard error, or the unit whose journal holds it. A renderer that
// reports refusals on standard output refuses the config with them, as a *refused, whether it
// failed or not; one that exits 0 and prints anything else there fails for what it printed, and
// one that fails and prints anything else fails for its exit status. A check's standard output is
// discarded: a service's own check can echo the files, so its failure says no more than that.
func (r Renderer) run(ctx context.Context, name string, what role, command string, stdin []byte, out string) error {
	timeout := cmp.Or(r.Timeout, DefaultTimeout)
	argv := strings.Fields(command)
	printed, err := r.Run.Run(ctx, Program{Argv: argv, Stdin: stdin, Out: out, Write: what == roleRenderer, Report: what == roleRenderer, Netlink: what == roleCheck, Timeout: timeout})
	failed, _ := errors.AsType[*ProgramError](err)
	if failed != nil {
		if failed.Unit != "" {
			log.Printf("output %s: its %s %s failed (%s); what it wrote to standard error is in the journal of %s", name, what, argv[0], failed.Status, failed.Unit)
		} else {
			log.Printf("output %s: its %s %s failed (%s): %q", name, what, argv[0], failed.Status, failed.Stderr)
		}
	}
	if what == roleRenderer && (err == nil || failed != nil) {
		rs, perr := refusals.Parse(printed)
		switch {
		case perr != nil && failed != nil:
			// A failed renderer's failure says more than what it printed instead of a report.
			log.Printf("output %s: its renderer %s printed no report of refusals: %v", name, argv[0], perr)
		case perr != nil:
			return fmt.Errorf("its renderer's standard output: %w", perr)
		case len(rs) > 0:
			return &refused{rs}
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("its %s did not finish within %s", what, timeout)
	case failed != nil:
		if what == roleCheck {
			return fmt.Errorf("its check refused the rendered files (%s); see the node's journal", failed.Status)
		}
		return fmt.Errorf("its renderer failed (%s); see the node's journal", failed.Status)
	}
	return fmt.Errorf("its %s: %w", what, err)
}

// checkFiles refuses an output that holds anything but directories and regular files, such as a
// link chalkd would follow when it sets the owner, or more than MaxSize bytes.
func checkFiles(dir string) error {
	var size int64
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case e.IsDir():
			return nil
		case !e.Type().IsRegular():
			return fmt.Errorf("its renderer wrote %s, which is no regular file", rel)
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if size += info.Size(); size > MaxSize {
			return fmt.Errorf("its renderer wrote more than %d bytes", MaxSize)
		}
		return nil
	})
}

// own gives the output's files its owner and mode, and its directories the owner and 0755; with
// keep set, the modes alone.
func own(dir string, d Definition, keep bool) error {
	mode, err := d.mode()
	if err != nil {
		return err
	}
	uid, gid := -1, -1
	if !keep {
		if uid, err = lookup(d.User, func(n string) (string, error) {
			u, err := user.Lookup(n)
			if err != nil {
				return "", err
			}
			return u.Uid, nil
		}); err != nil {
			return fmt.Errorf("its user %s: %w", d.User, err)
		}
		if gid, err = lookup(d.Group, func(n string) (string, error) {
			g, err := user.LookupGroup(n)
			if err != nil {
				return "", err
			}
			return g.Gid, nil
		}); err != nil {
			return fmt.Errorf("its group %s: %w", d.Group, err)
		}
	}
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !keep {
			if err := os.Lchown(path, uid, gid); err != nil {
				return err
			}
		}
		if e.IsDir() {
			return os.Chmod(path, 0o755)
		}
		return os.Chmod(path, mode)
	})
}

// lookup is the numeric ID of name, which is a number already or a name find resolves.
func lookup(name string, find func(string) (string, error)) (int, error) {
	if id, err := strconv.Atoi(name); err == nil {
		return id, nil
	}
	id, err := find(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(id)
}

// Join is the failures as one error, in the order given.
func Join(failures []Failure) error {
	errs := make([]error, len(failures))
	for i, f := range failures {
		errs[i] = f
	}
	return errors.Join(errs...)
}
