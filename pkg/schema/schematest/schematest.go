// Package schematest loads the fixture schemas of test/fixtures in tests.
package schematest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/schema"
)

// The fixture schema directories, relative to the repository's root.
const (
	// Builtin holds the schemas of the built-in kinds.
	Builtin = "test/fixtures/kinds/builtin"
	// Networkd holds those of networkd's Network and NetDev.
	Networkd = "test/fixtures/config/networkd/schemas"
	// Image holds that of the Demo extension kind, whose token is a secretRef.
	Image = "test/fixtures/kinds/image/schemas"
	// Router holds those of the router kinds.
	Router = "test/fixtures/kinds/router"
)

// Path is the path of rel, a path relative to the repository's root: the first directory up
// from the test's working directory that holds go.mod.
func Path(t testing.TB, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// Files are the .json files of the fixture directories by name, as an image's schemas/ holds
// them.
func Files(t testing.TB, dirs ...string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, dir := range dirs {
		dir = Path(t, dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = data
		}
	}
	return files
}

// Load compiles the schemas of the fixture directories.
func Load(t testing.TB, dirs ...string) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadFiles(Files(t, dirs...))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Copy writes the schemas of the fixture directories into dst.
func Copy(t testing.TB, dst string, dirs ...string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range Files(t, dirs...) {
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
