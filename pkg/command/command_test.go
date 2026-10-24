package command

import (
	"bytes"
	"context"
	"errors"
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
