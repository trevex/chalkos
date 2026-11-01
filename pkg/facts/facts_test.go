package facts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/command"
)

// fakeRunner answers commands from out, by program, and records what each read.
type fakeRunner struct {
	out    map[string]string
	err    error
	block  bool
	argv   [][]string
	stdins []map[string]any
	limits []int
}

func (f *fakeRunner) Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	f.argv = append(f.argv, argv)
	f.limits = append(f.limits, limit)
	var in map[string]any
	if err := json.Unmarshal(stdin, &in); err != nil {
		return nil, err
	}
	f.stdins = append(f.stdins, in)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte(f.out[argv[0]]), f.err
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.json")
	write(t, path, `{"share": {"file": "/state/kubernetes/share.json"}, "disks": {"command": "/nix/store/x/bin/chalkos-storage facts disks", "inputs": ["fallbackSecret"]}}`)
	defs, err := Read(path)
	want := map[string]Definition{
		"share": {File: "/state/kubernetes/share.json"},
		"disks": {Command: "/nix/store/x/bin/chalkos-storage facts disks", Inputs: []string{FallbackSecret}},
	}
	if err != nil || !reflect.DeepEqual(defs, want) {
		t.Errorf("Read = %+v, %v", defs, err)
	}
	if defs, err := Read(filepath.Join(dir, "missing.json")); err != nil || len(defs) != 0 {
		t.Errorf("a missing file: %v, %v; want no facts", defs, err)
	}
	// The content of an image's facts.json reads the same, as from an image that does not run.
	if parsed, err := Parse([]byte(`{"share": {"file": "/state/kubernetes/share.json"}, "disks": {"command": "/nix/store/x/bin/chalkos-storage facts disks", "inputs": ["fallbackSecret"]}}`)); err != nil || !reflect.DeepEqual(parsed, want) {
		t.Errorf("Parse = %+v, %v", parsed, err)
	}
	if _, err := Parse([]byte(`[]`)); err == nil {
		t.Error("Parse of no object: no error")
	}
}

// Check accepts a file with fields and a program of the store with the fallback secret, and refuses
// any other shape.
func TestDefinitionCheck(t *testing.T) {
	const lock = "/nix/store/x/bin/lock"
	for name, tc := range map[string]struct {
		def  Definition
		want string
	}{
		"a file":                {Definition{File: "/state/x.json", Fields: []string{"kind"}, HashOthers: true}, ""},
		"a command":             {Definition{Command: lock, Inputs: []string{FallbackSecret}}, ""},
		"neither":               {Definition{}, "it has either a file or a command"},
		"both":                  {Definition{File: "/state/x.json", Command: lock}, "it has either a file or a command"},
		"a command with fields": {Definition{Command: lock, Fields: []string{"kind"}}, "only a file keeps fields or hashes them"},
		"a file with inputs":    {Definition{File: "/state/x.json", Inputs: []string{FallbackSecret}}, "only a command takes inputs"},
		"outside the store":     {Definition{Command: "/usr/bin/lock"}, `the command "/usr/bin/lock" is no program of the image's store`},
		"arguments with paths":  {Definition{Command: lock + " --dir /var/lib/ --at /run//x"}, ""},
		"an unclean program":    {Definition{Command: "/nix/store/x/../../usr/bin/lock --dir /var/lib/"}, `the command "/nix/store/x/../../usr/bin/lock --dir /var/lib/" is no program of the image's store`},
		"a blank command":       {Definition{Command: " "}, `the command " " is no program of the image's store`},
		"an unknown input":      {Definition{Command: lock, Inputs: []string{"token"}}, `there is no input "token"; the inputs are fallbackSecret`},
	} {
		err := tc.def.Check()
		if got := fmt.Sprint(err); (tc.want == "" && err != nil) || (tc.want != "" && got != tc.want) {
			t.Errorf("%s: Check = %v, want %q", name, err, tc.want)
		}
	}
}

// A file fact is the file's JSON and null when the file is missing; at an install every fact,
// a command's too, is null.
func TestFileFact(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "share.json"), `{"kind": "worker", "port": 6443}`)
	write(t, filepath.Join(dir, "broken.json"), `{"kind": s3cret}`)
	defs := map[string]Definition{
		"share":   {File: filepath.Join(dir, "share.json")},
		"missing": {File: filepath.Join(dir, "missing.json")},
		"broken":  {File: filepath.Join(dir, "broken.json")},
	}
	l := NewLoader(context.Background(), defs, nil)
	if v, err := l.Fact("share"); err != nil || !reflect.DeepEqual(v, map[string]any{"kind": "worker", "port": json.Number("6443")}) {
		t.Errorf("share = %v, %v", v, err)
	}
	if v, err := l.Fact("missing"); err != nil || v != nil {
		t.Errorf("missing = %v, %v; want null", v, err)
	}
	if _, err := l.Fact("broken"); err == nil || err.Error() != filepath.Join(dir, "broken.json")+" is not JSON" {
		t.Errorf("broken: err = %v", err)
	}
	r := &fakeRunner{out: map[string]string{"/bin/disks": `{"extra": {}}`}}
	defs["disks"] = Definition{Command: "/bin/disks"}
	install := NewLoader(context.Background(), defs, r)
	install.Install = true
	for _, name := range []string{"share", "broken", "disks"} {
		if v, err := install.Fact(name); err != nil || v != nil {
			t.Errorf("%s at an install = %v, %v; want null", name, v, err)
		}
	}
	if len(r.argv) != 0 {
		t.Errorf("an install ran %v", r.argv)
	}
}

// A file fact with fields keeps those keys of the file's object alone.
func TestFileFactFields(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "share.json"), `{"kind": "worker", "caKey": "PRIVATE", "endpoint": "x"}`)
	write(t, filepath.Join(dir, "list.json"), `["PRIVATE"]`)
	l := NewLoader(context.Background(), map[string]Definition{
		"share": {File: filepath.Join(dir, "share.json"), Fields: []string{"kind", "type"}},
		"list":  {File: filepath.Join(dir, "list.json"), Fields: []string{"kind"}},
	}, nil)
	if v, err := l.Fact("share"); err != nil || !reflect.DeepEqual(v, map[string]any{"kind": "worker"}) {
		t.Errorf("share = %v, %v", v, err)
	}
	if _, err := l.Fact("list"); err == nil || strings.Contains(err.Error(), "PRIVATE") || !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("list: err = %v", err)
	}
}

// A file fact that hashes the others keeps its fields and the SHA-256 of every other key's value,
// a string as it is and anything else as its JSON; without fields it hashes every key.
func TestFileFactHashOthers(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "share.json"), `{"kind": "worker", "caKey": "PRIVATE", "ca": {"key": "PRIVATE"}}`)
	l := NewLoader(context.Background(), map[string]Definition{
		"share":  {File: filepath.Join(dir, "share.json"), Fields: []string{"kind"}, HashOthers: true},
		"hashes": {File: filepath.Join(dir, "share.json"), HashOthers: true},
	}, nil)
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	want := map[string]any{"kind": "worker", "caKey": sum("PRIVATE"), "ca": sum(`{"key":"PRIVATE"}`)}
	if v, err := l.Fact("share"); err != nil || !reflect.DeepEqual(v, want) {
		t.Errorf("share = %v, %v; want %v", v, err, want)
	}
	want["kind"] = sum("worker")
	if v, err := l.Fact("hashes"); err != nil || !reflect.DeepEqual(v, want) {
		t.Errorf("hashes = %v, %v; want %v", v, err, want)
	}
}

// A loader For another input shares the delivery's facts: a file is read once, and a command runs
// once for each input it reads. Overrides stay with the loader that made them.
func TestLoaderFor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recorded.json")
	write(t, path, `{"volumes": {}}`)
	r := &fakeRunner{out: map[string]string{"/bin/disks": `{}`}}
	l := NewLoader(context.Background(), map[string]Definition{
		"recorded": {File: path},
		"share":    {File: filepath.Join(dir, "share.json")},
		"disks":    {Command: "/bin/disks"},
	}, r)
	l.Input = map[string]any{"config": map[string]any{"a": "b"}}
	l.Override("share", "overridden")
	for _, name := range []string{"recorded", "disks"} {
		if _, err := l.Fact(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	same := l.For(map[string]any{"config": map[string]any{"a": "b"}})
	other := l.For(map[string]any{"config": map[string]any{"a": "c"}})
	for _, f := range []*Loader{same, other} {
		if v, err := f.Fact("recorded"); err != nil || v == nil {
			t.Errorf("recorded = %v, %v; want the file read once", v, err)
		}
		if _, err := f.Fact("disks"); err != nil {
			t.Fatal(err)
		}
		if v, err := f.Fact("share"); err != nil || v != nil {
			t.Errorf("share = %v, %v; want the override left behind", v, err)
		}
	}
	if len(r.stdins) != 2 || r.stdins[1]["config"].(map[string]any)["a"] != "c" {
		t.Errorf("the command read %v, want once for each input", r.stdins)
	}
}

// A command reads the config and only the inputs it declares, and prints its fact as JSON. A
// delivery loads each fact once; a fact the image does not declare is null and runs nothing.
func TestCommandFact(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"/bin/disks": `{"extra": {"present": true}}`, "/bin/fallback": `{"matches": true}`}}
	l := NewLoader(context.Background(), map[string]Definition{
		"disks":    {Command: "/bin/disks facts disks"},
		"fallback": {Command: "/bin/fallback", Inputs: []string{FallbackSecret}},
	}, r)
	l.Input = map[string]any{"config": map[string]any{"a": "b"}, "oldConfig": map[string]any{}}
	l.Inputs = map[string]any{FallbackSecret: "s3cret"}
	for range 2 {
		if v, err := l.Fact("disks"); err != nil || !reflect.DeepEqual(v, map[string]any{"extra": map[string]any{"present": true}}) {
			t.Errorf("disks = %v, %v", v, err)
		}
	}
	if _, err := l.Fact("fallback"); err != nil {
		t.Fatal(err)
	}
	if v, err := l.Fact("undeclared"); err != nil || v != nil {
		t.Errorf("undeclared = %v, %v; want null", v, err)
	}
	if want := [][]string{{"/bin/disks", "facts", "disks"}, {"/bin/fallback"}}; !reflect.DeepEqual(r.argv, want) {
		t.Errorf("ran %v, want %v", r.argv, want)
	}
	if _, ok := r.stdins[0][FallbackSecret]; ok || !reflect.DeepEqual(r.stdins[0]["config"], map[string]any{"a": "b"}) {
		t.Errorf("disks read %v; want the config without the secret", r.stdins[0])
	}
	if r.stdins[1][FallbackSecret] != "s3cret" {
		t.Errorf("fallback read %v; want the secret it declares", r.stdins[1])
	}
	if !slices.Equal(r.limits, []int{command.MaxOutput, command.MaxOutput}) {
		t.Errorf("limits = %v", r.limits)
	}
}

// An override replaces a fact for the delivery and loads nothing.
func TestOverride(t *testing.T) {
	r := &fakeRunner{}
	l := NewLoader(context.Background(), map[string]Definition{"disks": {Command: "/bin/disks"}}, r)
	l.Override("disks", map[string]any{"x": true})
	if v, err := l.Fact("disks"); err != nil || !reflect.DeepEqual(v, map[string]any{"x": true}) || len(r.argv) != 0 {
		t.Errorf("disks = %v, %v; ran %v", v, err, r.argv)
	}
}

// The sandbox runs the command as a transient unit that cannot change the node.
func TestSandboxed(t *testing.T) {
	want := []string{"systemd-run", "--pipe", "--wait", "--quiet", "--collect",
		"--property=ProtectSystem=strict", "--property=ReadOnlyPaths=-/state",
		"--property=ProtectHome=yes",
		"--property=PrivateTmp=yes", "--property=ProtectKernelTunables=yes",
		"--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		"--property=PrivateNetwork=yes", "--property=RestrictAddressFamilies=AF_UNIX AF_ALG",
		"--property=NoNewPrivileges=yes", "--property=RuntimeMaxSec=30",
		"--property=ReadWritePaths=-/run/cryptsetup", "--",
		"/nix/store/x/bin/chalkos-storage", "facts", "disks"}
	if got, err := (Sandbox{}).command([]string{"/nix/store/x/bin/chalkos-storage", "facts", "disks"}); err != nil || !slices.Equal(got, want) {
		t.Errorf("command = %v, %v", got, err)
	}
	// A fact of an image that does not run: its store over /nix/store, its program run from where
	// the store is on the node, which systemd-run finds, its links followed within that store as
	// nixpkgs' bin/ links into another package.
	store := filepath.Join(t.TempDir(), "nix", "store")
	for _, dir := range []string{"x/bin", "y/bin"} {
		if err := os.MkdirAll(filepath.Join(store, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(store, "y/bin/chalkos-storage"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"x/bin/chalkos-storage": "/nix/store/y/bin/chalkos-storage", "x/bin/out": "/etc/passwd"} {
		if err := os.Symlink(target, filepath.Join(store, link)); err != nil {
			t.Fatal(err)
		}
	}
	want = append(slices.Clone(want[:len(want)-4]), "--property=BindReadOnlyPaths="+store+":/nix/store", "--",
		store+"/y/bin/chalkos-storage", "facts", "disks")
	if got, err := (Sandbox{Store: store}).command([]string{"/nix/store/x/bin/chalkos-storage", "facts", "disks"}); err != nil || !slices.Equal(got, want) {
		t.Errorf("command = %v, %v\nwant %v", got, err, want)
	}
	// A link out of the image's store leads nowhere: the node's own file is no file of the image.
	if _, err := (Sandbox{Store: store}).command([]string{"/nix/store/x/bin/out"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a link out of the store: err = %v", err)
	}
}

// A command is a program of the image's store, given as a clean path: a .. would lead out of the
// store a sandbox joins it to.
func TestCommandInStore(t *testing.T) {
	for cmd, ok := range map[string]bool{
		"/nix/store/x/bin/disks":           true,
		"/nix/store/x/../../../etc/passwd": false,
		"/nix/store/../store/x/bin/disks":  false,
		"/nix/store/x//bin/disks":          false,
		"/nix/store/":                      false,
		"/usr/bin/disks":                   false,
	} {
		if err := (Definition{Command: cmd}).Check(); (err == nil) != ok {
			t.Errorf("%s: err = %v", cmd, err)
		}
	}
}

// A command that fails, or prints no JSON, refuses with an error that shows nothing of what it
// wrote, which can echo the config it reads; what it wrote goes to the journal.
func TestCommandFailures(t *testing.T) {
	defs := map[string]Definition{"disks": {Command: "/bin/disks"}}
	for name, tc := range map[string]struct {
		r         *fakeRunner
		want, log string
	}{
		"exit status": {&fakeRunner{err: &command.Error{Status: "exit 1", Stderr: "no disk for s3cret"}},
			"the command failed (exit 1); see the node's journal", `fact disks: /bin/disks failed (exit 1): "no disk for s3cret"`},
		"not JSON": {&fakeRunner{out: map[string]string{"/bin/disks": "s3cret: ok"}},
			"/bin/disks printed no JSON; see the node's journal", "fact disks: /bin/disks printed no JSON: invalid character"},
		"two values": {&fakeRunner{out: map[string]string{"/bin/disks": "{} {}"}},
			"/bin/disks printed no JSON; see the node's journal", "more than one JSON value"},
		"too long": {&fakeRunner{out: map[string]string{"/bin/disks": `"` + strings.Repeat("x", command.MaxOutput) + `"`}},
			"/bin/disks printed more than 1048576 bytes", ""},
		"timeout": {&fakeRunner{block: true}, "/bin/disks did not finish within 50ms", ""},
	} {
		t.Run(name, func(t *testing.T) {
			var logged bytes.Buffer
			log.SetOutput(&logged)
			defer log.SetOutput(os.Stderr)
			l := NewLoader(context.Background(), defs, tc.r)
			l.Timeout = 50 * time.Millisecond
			if _, err := l.Fact("disks"); err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if !strings.Contains(logged.String(), tc.log) {
				t.Errorf("logged %q, want %q", logged.String(), tc.log)
			}
		})
	}
}
