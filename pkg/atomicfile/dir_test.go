package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenameNewRefusesAnOutputThatAppeared leaves an output that appeared after CheckNew as it is,
// even an empty directory, which a plain rename would replace.
func TestRenameNewRefusesAnOutputThatAppeared(t *testing.T) {
	dir := t.TempDir()
	tmp, out := filepath.Join(dir, "tmp"), filepath.Join(dir, "out")
	for _, d := range []string{tmp, out} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameNew(tmp, out); err == nil || !strings.Contains(err.Error(), "appeared") {
		t.Errorf("renameNew onto an empty directory = %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("renameNew moved tmp: %v", err)
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	if err := renameNew(tmp, out); err != nil {
		t.Fatal(err)
	}
}
