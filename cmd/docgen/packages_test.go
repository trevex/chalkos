package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Go directory of cmd/ or pkg/ needs a row of its own; directories without Go code, with tests
// alone or below testdata need none.
func TestCheckPackages(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{
		"cmd/tool/main.go",
		"pkg/tool/tool.go",
		"pkg/tool/sub/sub.go",
		"pkg/tool/sub/sub_test.go",
		"pkg/tool/testdata/fixture/fixture.go",
		"pkg/api/v1/api.go",
		"pkg/onlytests/x_test.go",
		"pkg/data/cert.der",
		"pkg/unlisted/unlisted.go",
	} {
		path := filepath.Join(root, file)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, nil, 0o644)
	}
	page := filepath.Join(root, "packages.md")
	os.WriteFile(page, []byte("| Program | What |\n| --- | --- |\n| `cmd/tool` | Runs `pkg/unlisted`. |\n\n"+
		"| Package | What |\n| --- | --- |\n| `tool` | The tool. |\n| `tool/sub`, `api/v1` | Its parts. |\n"), 0o644)

	err := checkPackages(page, root)
	if err == nil || !strings.HasSuffix(err.Error(), ":\npkg/unlisted") {
		t.Fatalf("checkPackages = %v, want pkg/unlisted alone missing", err)
	}
	os.WriteFile(page, []byte("| Package | What |\n| --- | --- |\n| `cmd/tool` | |\n| `tool`, `tool/sub`, `api/v1`, `unlisted` | |\n"), 0o644)
	if err := checkPackages(page, root); err != nil {
		t.Errorf("checkPackages with every row = %v", err)
	}
}
