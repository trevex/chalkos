package command

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run passes stdin, returns standard output and refuses more than the limit. A program that fails
// is an Error with its exit status, or the signal that ended it, and the start of what it wrote
// to standard error, which its error does not show.
func TestRun(t *testing.T) {
	ctx := context.Background()
	out, err := Run(ctx, []string{"sh", "-c", "cat"}, []byte(`{"a": 1}`), 100)
	if err != nil || !bytes.Equal(out, []byte(`{"a": 1}`)) {
		t.Errorf("cat = %q, %v", out, err)
	}
	if _, err := Run(ctx, []string{"sh", "-c", "head -c 200 /dev/zero"}, nil, 100); err == nil || !strings.Contains(err.Error(), "more than 100 bytes") {
		t.Errorf("long output: err = %v", err)
	}
	for name, tc := range map[string]struct {
		script, status, stderr string
	}{
		"exit":   {"cat >&2; exit 3", "exit 3", `{"config": "s3cret"}`},
		"signal": {"cat >&2; kill -9 $$", "signal: killed", `{"config": "s3cret"}`},
		"long":   {"head -c 10000 /dev/zero | tr '\\0' x >&2; exit 1", "exit 1", strings.Repeat("x", maxStderr)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Run(ctx, []string{"sh", "-c", tc.script}, []byte(`{"config": "s3cret"}`), 100)
			var failed *Error
			if !errors.As(err, &failed) || failed.Status != tc.status || failed.Stderr != tc.stderr {
				t.Fatalf("err = %#v", err)
			}
			if want := "the command failed (" + tc.status + "); see the node's journal"; err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
		})
	}
}

// ResolveInStore follows a program's links within the image's store, absolute and relative, and
// refuses one that leads out of it.
func TestResolveInStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(filepath.Join(store, "b", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "b", "bin", "x"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"a": "/nix/store/b", "r": "b/bin/../bin", "out": "/etc", "up": "../../etc"} {
		if err := os.Symlink(target, filepath.Join(store, link)); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{"/nix/store/a/bin/x": "b/bin/x", "/nix/store/r/x": "b/bin/x", "/nix/store/b/bin/x": "b/bin/x"} {
		if got, err := ResolveInStore(store, name); err != nil || got != filepath.Join(store, want) {
			t.Errorf("%s = %s, %v", name, got, err)
		}
	}
	for _, name := range []string{"/nix/store/out/passwd", "/nix/store/up/passwd", "/nix/store/b/bin/missing"} {
		if _, err := ResolveInStore(store, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
