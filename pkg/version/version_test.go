package version

import (
	"bytes"
	"testing"
)

// TestCommand prints the version and the revision, each on a line of its own.
func TestCommand(t *testing.T) {
	Version, Revision = "1.2.3", "abc123-dirty"
	cmd := Command("chalkctl")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "version 1.2.3\nrevision abc123-dirty\n"; out.String() != want {
		t.Errorf("version printed %q, want %q", out.String(), want)
	}
}
