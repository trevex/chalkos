package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestFile writes data to path below root, creating its directories.
func writeTestFile(t *testing.T, root, path, data string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Go directory of cmd/ or pkg/ needs a row of its own; directories without Go code, with tests
// alone or below testdata need none, and a row naming one is stale.
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
		writeTestFile(t, root, file, "")
	}
	page := filepath.Join(root, "packages.md")

	writeTestFile(t, root, "packages.md", "| Program | What |\n| --- | --- |\n| `cmd/tool` | Runs `pkg/unlisted`. |\n\n"+
		"| Package | What |\n| --- | --- |\n| `tool` | The tool. |\n| `tool/sub`, `api/v1` | Its parts. |\n")
	err := checkPackages(page, root)
	if err == nil || !strings.HasSuffix(err.Error(), ":\npkg/unlisted") || strings.Contains(err.Error(), "remove them") {
		t.Fatalf("checkPackages = %v, want pkg/unlisted alone missing", err)
	}

	writeTestFile(t, root, "packages.md", "| Package | What |\n| --- | --- |\n| `cmd/tool` | |\n| `tool`, `tool/sub`, `api/v1`, `unlisted` | |\n")
	if err := checkPackages(page, root); err != nil {
		t.Errorf("checkPackages with every row = %v", err)
	}

	writeTestFile(t, root, "packages.md", "| Package | What |\n| --- | --- |\n| `cmd/tool`, `cmd/gone` | |\n| `tool`, `tool/sub`, `api/v1`, `unlisted` | |\n| `onlytests`, `data` | |\n")
	err = checkPackages(page, root)
	if err == nil || !strings.HasSuffix(err.Error(), "remove them:\ncmd/gone\npkg/data\npkg/onlytests") || strings.Contains(err.Error(), "has no row") {
		t.Errorf("checkPackages with stale rows = %v, want cmd/gone, pkg/data and pkg/onlytests named", err)
	}
}
