package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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
}

// A file fact is the file's JSON, null when the file is missing, and null for every file on the
// installer.
func TestFileFact(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "share.json"), `{"kind": "worker", "port": 6443}`)
	write(t, filepath.Join(dir, "broken.json"), `{"kind":`)
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
	if _, err := l.Fact("broken"); err == nil || !strings.Contains(err.Error(), "broken.json is not JSON") {
		t.Errorf("broken: err = %v", err)
	}
	installer := NewLoader(context.Background(), defs, nil)
	installer.NoFiles = true
	if v, err := installer.Fact("share"); err != nil || v != nil {
		t.Errorf("share on the installer = %v, %v; want null", v, err)
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
	if !slices.Equal(r.limits, []int{MaxOutput, MaxOutput}) {
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

func TestCommandFailures(t *testing.T) {
	defs := map[string]Definition{"disks": {Command: "/bin/disks"}}
	for name, tc := range map[string]struct {
		r    *fakeRunner
		want string
	}{
		"exit status": {&fakeRunner{err: errors.New("exit status 1: no disk")}, "exit status 1: no disk"},
		"not JSON":    {&fakeRunner{out: map[string]string{"/bin/disks": "disks: ok"}}, "/bin/disks printed no JSON"},
		"two values":  {&fakeRunner{out: map[string]string{"/bin/disks": "{} {}"}}, "more than one JSON value"},
		"too long":    {&fakeRunner{out: map[string]string{"/bin/disks": `"` + strings.Repeat("x", MaxOutput) + `"`}}, "printed more than 1048576 bytes"},
		"timeout":     {&fakeRunner{block: true}, "/bin/disks did not finish within 50ms"},
	} {
		t.Run(name, func(t *testing.T) {
			l := NewLoader(context.Background(), defs, tc.r)
			l.Timeout = 50 * time.Millisecond
			if _, err := l.Fact("disks"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// The sandbox runs the command as a transient unit that cannot change the node.
func TestSandboxed(t *testing.T) {
	want := []string{"systemd-run", "--pipe", "--wait", "--quiet", "--collect",
		"--property=ProtectSystem=strict", "--property=ReadOnlyPaths=/state",
		"--property=ReadWritePaths=-/run/cryptsetup", "--property=PrivateNetwork=yes",
		"--property=NoNewPrivileges=yes", "--property=RuntimeMaxSec=30", "--",
		"/nix/store/x/bin/chalkos-storage", "facts", "disks"}
	if got := sandboxed([]string{"/nix/store/x/bin/chalkos-storage", "facts", "disks"}); !slices.Equal(got, want) {
		t.Errorf("sandboxed = %v", got)
	}
}

// run passes stdin, returns standard output, refuses more than the limit and names what the
// program wrote to standard error when it fails.
func TestRun(t *testing.T) {
	ctx := context.Background()
	out, err := run(ctx, []string{"sh", "-c", "cat"}, []byte(`{"a": 1}`), 100)
	if err != nil || !bytes.Equal(out, []byte(`{"a": 1}`)) {
		t.Errorf("cat = %q, %v", out, err)
	}
	if _, err := run(ctx, []string{"sh", "-c", "head -c 200 /dev/zero"}, nil, 100); err == nil || !strings.Contains(err.Error(), "more than 100 bytes") {
		t.Errorf("long output: err = %v", err)
	}
	if _, err := run(ctx, []string{"sh", "-c", "echo no disk >&2; exit 3"}, nil, 100); err == nil || err.Error() != "exit status 3: no disk" {
		t.Errorf("failure: err = %v", err)
	}
}
