package image

import (
	"os"
	"path/filepath"
	"testing"
)

// FindRaw takes the directory's path literally, so a bracket in it is no pattern.
func TestFindRawBracketInPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "image [1]")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "chalkos.raw")
	if err := os.WriteFile(want, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := FindRaw(dir); err != nil || got != want {
		t.Errorf("FindRaw = %q, %v; want %q", got, err, want)
	}
}
