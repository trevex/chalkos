// Package outputs renders a service's config files from the node's config documents: each output
// of /etc/chalkos/outputs.json is a store program that reads the documents of its input kinds as
// JSON and writes files into a directory, and an optional check of those files. chalkd renders
// and checks every output into a staging directory before a delivery changes the node, and swaps
// each one in by a symlink rename once the delivery is stored, so a service never reads half an
// output; chalkos-config renders them at boot, since /run is a tmpfs.
package outputs

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/schema"
)

const (
	// DefaultPath is where an image declares its outputs, and DefaultRoot where a node renders
	// them: each output's files below DefaultRoot/.rendered, linked from the output's dir.
	DefaultPath = "/etc/chalkos/outputs.json"
	DefaultRoot = "/run/chalkos/outputs"
	// MaxSize bounds the bytes of an output's files, which land on /run, a tmpfs.
	MaxSize = 16 << 20
)

// Definition is an output as outputs.json declares it.
type Definition struct {
	// Inputs are the kinds, by name, whose documents the renderer reads.
	Inputs []string `json:"inputs"`
	// Render is a program of the image's store with its arguments, which writes the output's
	// files into $OUT; Check, optional, a program that reads them there and fails to refuse them.
	Render string `json:"render"`
	Check  string `json:"check,omitempty"`
	// Dir is where services read the files: a link to the directory that holds them.
	Dir string `json:"dir"`
	// User, Group and Mode are the owner and the octal mode of the files.
	User  string `json:"user"`
	Group string `json:"group"`
	Mode  string `json:"mode"`
}

// Read reads the outputs an image declares; a missing file declares none.
func Read(path string) (map[string]Definition, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Definition{}, nil
	}
	if err != nil {
		return nil, err
	}
	var defs map[string]Definition
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		if err := defs[name].check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: output %s: %w", path, name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return defs, nil
}

// check refuses a definition chalkd could not carry out: a program that is no absolute path, a
// relative dir or a mode that is no octal number. The image's build checks that the programs are
// of its store.
func (d Definition) check() error {
	for _, p := range []struct{ what, command string }{{"renderer", d.Render}, {"check", d.Check}} {
		if p.what == "check" && p.command == "" {
			continue
		}
		if argv := strings.Fields(p.command); len(argv) == 0 || !filepath.IsAbs(argv[0]) {
			return fmt.Errorf("the %s %q names no program by its absolute path", p.what, p.command)
		}
	}
	if !filepath.IsAbs(d.Dir) || filepath.Clean(d.Dir) != d.Dir {
		return fmt.Errorf("the dir %q is no absolute, clean path", d.Dir)
	}
	if _, err := d.mode(); err != nil {
		return err
	}
	return nil
}

func (d Definition) mode() (fs.FileMode, error) {
	m, err := strconv.ParseUint(d.Mode, 8, 32)
	if err != nil || m > 0o777 {
		return 0, fmt.Errorf("the mode %q is no octal file mode", d.Mode)
	}
	return fs.FileMode(m), nil
}

// Runner runs a renderer or a check: argv with stdin, and $OUT naming out, the only path it may
// write. A program that fails is a ProgramError.
type Runner interface {
	Run(ctx context.Context, argv []string, stdin []byte, out string) error
}

// A ProgramError is a renderer or a check that failed. Status is "exit N", or why systemd
// stopped it; Stderr is the start of what it wrote to standard error, or, in the sandbox, empty,
// and Unit names the transient unit whose journal holds it.
type ProgramError struct {
	Status, Stderr, Unit string
}

func (e *ProgramError) Error() string { return "the program failed (" + e.Status + ")" }

// Sandbox runs programs in the facts' sandbox, with out writable as well, as oneshot transient
// units that write standard error to the journal. chalkos-config renders before sysinit.target,
// where the system bus does not run yet, so systemd-run reaches the manager on its private
// socket, on which it cannot follow a unit as --wait and --pipe do: the start job of a oneshot
// unit finishes when the program exits, the program reads standard input from a file, and its
// exit status is the unit's. The units have no default dependencies, which would wait for
// sysinit.target.
type Sandbox struct{}

// manager reaches systemd's manager without the system bus.
const manager = "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/systemd/private"

func (Sandbox) Run(ctx context.Context, argv []string, stdin []byte, out string) error {
	input := out + ".input"
	if err := os.WriteFile(input, stdin, 0o600); err != nil {
		return err
	}
	defer os.Remove(input)
	unit := "chalkos-render-" + filepath.Ext(out)[1:] + ".service"
	args := append([]string{"systemd-run", "--quiet", "--service-type=oneshot", "--unit=" + unit}, facts.SandboxProperties()...)
	args = append(args,
		"--property=DefaultDependencies=no",
		// The start of a oneshot unit lasts until its program exits.
		fmt.Sprintf("--property=TimeoutStartSec=%d", int(facts.Timeout.Seconds())),
		"--property=ReadWritePaths="+out,
		"--setenv=OUT="+out,
		"--property=StandardInput=file:"+input,
		"--property=StandardOutput=null",
		"--property=StandardError=journal",
		"--",
	)
	_, err := facts.RunCommand(ctx, append(args, argv...), nil, facts.MaxOutput, manager)
	var exit *facts.CommandError
	if !errors.As(err, &exit) {
		return err
	}
	// A unit that failed stays loaded, with its result, until it is reset.
	show, serr := facts.RunCommand(context.Background(), []string{"systemctl", "show", "--property=Result", "--property=ExecMainStatus", unit}, nil, facts.MaxOutput, manager)
	facts.RunCommand(context.Background(), []string{"systemctl", "reset-failed", unit}, nil, facts.MaxOutput, manager)
	if serr != nil {
		return &ProgramError{Status: exit.Status, Unit: unit}
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(show), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	status := "exit " + props["ExecMainStatus"]
	if r := props["Result"]; r != "exit-code" && r != "" {
		status = r
	}
	return &ProgramError{Status: status, Unit: unit}
}

// Direct runs programs as they are, as the image's build and tests do.
type Direct struct{}

func (Direct) Run(ctx context.Context, argv []string, stdin []byte, out string) error {
	_, err := facts.RunCommand(ctx, argv, stdin, facts.MaxOutput, "OUT="+out)
	var failed *facts.CommandError
	if errors.As(err, &failed) {
		return &ProgramError{Status: failed.Status, Stderr: failed.Stderr}
	}
	return err
}

// Input is what outputs render from: a node's config set, with its defaults, the schemas that
// tell its secretRef fields, and the node's name.
type Input struct {
	Node      string
	Documents []config.Document
	Schemas   *schema.Schemas
}

// stdin is what a renderer reads: the node's name and the documents of each input kind, sorted by
// name, as rules see them, with their schema defaults. The renderer never sees a Secret's values.
func (d Definition) stdin(in Input) ([]byte, error) {
	docs := map[string][]any{}
	for _, kind := range d.Inputs {
		docs[kind] = []any{}
	}
	for _, doc := range d.documents(in) {
		docs[doc.Kind] = append(docs[doc.Kind], doc.Hashed())
	}
	return json.Marshal(map[string]any{
		"node":      map[string]any{"name": in.Node},
		"documents": docs,
	})
}

// documents are the documents of the input kinds, sorted by name.
func (d Definition) documents(in Input) []config.Document {
	var out []config.Document
	for _, doc := range in.Documents {
		if !doc.IsSecret() && slices.Contains(d.Inputs, doc.Kind) {
			out = append(out, doc)
		}
	}
	slices.SortStableFunc(out, func(a, b config.Document) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// State is what tells that an output changed: the hash of its files, and the hash of the Secret
// values its input documents reference, which a service reads as credentials beside the files.
type State struct {
	Files   string `json:"files"`
	Secrets string `json:"secrets"`
}

// secrets hashes the values the secretRefs of the output's input documents name, by Secret and
// key; empty for documents that reference none.
func (d Definition) secrets(in Input) string {
	data := map[string]map[string]any{}
	for _, doc := range in.Documents {
		if doc.IsSecret() {
			spec, _ := doc.Object["spec"].(map[string]any)
			values, _ := spec["data"].(map[string]any)
			data[doc.Name] = values
		}
	}
	var lines []string
	for _, doc := range d.documents(in) {
		for _, ref := range config.SecretRefs(doc, in.Schemas) {
			v, _ := data[ref.Name][ref.Key].(string)
			sum := sha256.Sum256([]byte(v))
			lines = append(lines, ref.Name+"\x00"+ref.Key+"\x00"+hex.EncodeToString(sum[:]))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Live is the state of the output the node serves now, before a delivery replaces it: the hash of
// the files its dir holds, empty when it holds none, and the Secrets in reads.
func Live(d Definition, in Input) (State, error) {
	files, err := hashDir(d.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		files, err = "", nil
	}
	if err != nil {
		return State{}, err
	}
	return State{Files: files, Secrets: d.secrets(in)}, nil
}

// hashDir hashes the names and contents of the regular files below dir.
func hashDir(dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		content := sha256.New()
		if _, err := io.Copy(content, f); err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%x\n", rel, content.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// A Failure is an output that could not be rendered, or whose check refused it. Its problem names
// what failed and its exit status alone: what the program wrote to standard error goes to the
// journal, since it can echo the documents it read.
type Failure struct {
	Output, Problem string
}

func (f Failure) Error() string { return "output " + f.Output + ": " + f.Problem }

// Renderer renders outputs below Root.
type Renderer struct {
	Root string
	Run  Runner
	// Timeout bounds each program; zero is facts.Timeout.
	Timeout time.Duration
}

// Default renders on the node, in the sandbox.
func Default() Renderer {
	return Renderer{Root: DefaultRoot, Run: Sandbox{}}
}

// Staged are rendered and checked outputs, waiting below Root/.staging to be swapped in.
type Staged struct {
	root    string
	defs    map[string]Definition
	staging map[string]string
	states  map[string]State
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
		failures = append(failures, Failure{Output: name, Problem: err.Error()})
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
	if err := r.run(ctx, name, "renderer", d.Render, stdin, dir); err != nil {
		return dir, State{}, err
	}
	if err := checkFiles(dir); err != nil {
		return dir, State{}, err
	}
	if err := own(dir, d); err != nil {
		return dir, State{}, err
	}
	if d.Check != "" {
		if err := r.run(ctx, name, "check", d.Check, stdin, dir); err != nil {
			return dir, State{}, err
		}
	}
	files, err := hashDir(dir)
	if err != nil {
		return dir, State{}, err
	}
	return dir, State{Files: files, Secrets: d.secrets(in)}, nil
}

// run runs a renderer or a check of the output name, logging what a failed program wrote to
// standard error, or the unit whose journal holds it.
func (r Renderer) run(ctx context.Context, name, what, command string, stdin []byte, out string) error {
	timeout := cmp.Or(r.Timeout, facts.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := strings.Fields(command)
	err := r.Run.Run(ctx, argv, stdin, out)
	var failed *ProgramError
	switch {
	case err == nil:
		return nil
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("its %s did not finish within %s", what, timeout)
	case errors.As(err, &failed):
		if failed.Unit != "" {
			log.Printf("output %s: its %s %s failed (%s); what it wrote to standard error is in the journal of %s", name, what, argv[0], failed.Status, failed.Unit)
		} else {
			log.Printf("output %s: its %s %s failed (%s): %q", name, what, argv[0], failed.Status, failed.Stderr)
		}
		if what == "check" {
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

// own gives the output's files its owner and mode, and its directories the owner and 0755.
func own(dir string, d Definition) error {
	uid, err := lookup(d.User, func(n string) (string, error) {
		u, err := user.Lookup(n)
		if err != nil {
			return "", err
		}
		return u.Uid, nil
	})
	if err != nil {
		return fmt.Errorf("its user %s: %w", d.User, err)
	}
	gid, err := lookup(d.Group, func(n string) (string, error) {
		g, err := user.LookupGroup(n)
		if err != nil {
			return "", err
		}
		return g.Gid, nil
	})
	if err != nil {
		return fmt.Errorf("its group %s: %w", d.Group, err)
	}
	mode, err := d.mode()
	if err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return err
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

// States are the states of the staged outputs, by name.
func (s *Staged) States() map[string]State {
	return maps.Clone(s.states)
}

// Swap makes each staged output the one its dir links to: the files move below Root/.rendered,
// and a new link replaces the old one in one rename, so a service reads the old files or the new
// ones and never a mix. The files the old link named are removed. It records each swapped
// output's hash in the status.
func (s *Staged) Swap() error {
	if len(s.staging) == 0 {
		return nil
	}
	rendered := filepath.Join(s.root, ".rendered")
	if err := os.MkdirAll(rendered, 0o755); err != nil {
		return err
	}
	var errs []error
	swapped := map[string]State{}
	for _, name := range slices.Sorted(maps.Keys(s.staging)) {
		if err := s.swap(name, rendered); err != nil {
			errs = append(errs, fmt.Errorf("output %s: swap in the rendered files: %w", name, err))
			continue
		}
		swapped[name] = s.states[name]
	}
	if err := updateStatus(s.root, func(st map[string]Status) {
		for name, state := range swapped {
			st[name] = Status{Hash: state.Files}
		}
	}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Staged) swap(name, rendered string) error {
	dir := s.defs[name].Dir
	target := filepath.Join(rendered, filepath.Base(s.staging[name]))
	if err := os.Rename(s.staging[name], target); err != nil {
		return err
	}
	delete(s.staging, name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	old, _ := os.Readlink(dir)
	if info, err := os.Lstat(dir); err == nil && info.IsDir() {
		// A directory an earlier image or a tmpfiles rule made, which no rename replaces.
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	link := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".link")
	os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	if err := os.Rename(link, dir); err != nil {
		os.Remove(link)
		return err
	}
	if old != "" && old != target && filepath.Dir(old) == rendered {
		if err := os.RemoveAll(old); err != nil {
			log.Printf("output %s: remove the files it replaced: %v", name, err)
		}
	}
	return nil
}

// Discard removes the staged outputs that were not swapped in.
func (s *Staged) Discard() {
	for name, dir := range s.staging {
		os.RemoveAll(dir)
		delete(s.staging, name)
	}
}

// Status is what a node reports of an output: the hash of the files it serves, and why its last
// render or check failed, empty once one passed.
type Status struct {
	Hash  string `json:"hash,omitempty"`
	Error string `json:"error,omitempty"`
}

func statusPath(root string) string { return filepath.Join(root, ".status.json") }

// ReadStatus reads the outputs' status below root; none was recorded on a node that rendered
// none.
func ReadStatus(root string) (map[string]Status, error) {
	data, err := os.ReadFile(statusPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Status{}, nil
	}
	if err != nil {
		return nil, err
	}
	st := map[string]Status{}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", statusPath(root), err)
	}
	return st, nil
}

// RecordFailures records the failures in the status below root; the hash each output serves
// stays.
func RecordFailures(root string, failures []Failure) error {
	return updateStatus(root, func(st map[string]Status) {
		for _, f := range failures {
			s := st[f.Output]
			s.Error = f.Problem
			st[f.Output] = s
		}
	})
}

func updateStatus(root string, update func(map[string]Status)) error {
	st, err := ReadStatus(root)
	if err != nil {
		// A status that cannot be read is replaced.
		log.Printf("the outputs' status: %v", err)
		st = map[string]Status{}
	}
	update(st)
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp := statusPath(root) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statusPath(root))
}

// Join is the failures as one error, in the order given.
func Join(failures []Failure) error {
	errs := make([]error, len(failures))
	for i, f := range failures {
		errs[i] = f
	}
	return errors.Join(errs...)
}
