package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// readTree returns every file below dir by relative path; symlinks map to their target.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			files[rel] = "-> " + target
			return err
		}
		data, err := os.ReadFile(path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestGenerateGolden(t *testing.T) {
	out := t.TempDir()
	err := runGenerate([]string{
		"--state", "testdata/generate/state",
		"--cryptsetup", "/run/current-system/sw/bin/systemd-cryptsetup",
		out, out + "/early", out + "/late",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readTree(t, out)
	want := readTree(t, "testdata/generate/units")
	// Symlinks cannot live in testdata portably, so the golden tree lists them in a file.
	links, err := os.ReadFile("testdata/generate/links")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(links)), "\n") {
		name, target, _ := strings.Cut(line, " -> ")
		want[name] = "-> " + target
	}

	for _, name := range sortedKeys(want) {
		if got[name] != want[name] {
			t.Errorf("%s:\n got: %q\nwant: %q", name, got[name], want[name])
		}
	}
	for _, name := range sortedKeys(got) {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected %s:\n%s", name, got[name])
		}
	}
}

func TestGenerateWithoutSection(t *testing.T) {
	out := t.TempDir()
	if err := runGenerate([]string{"--state", t.TempDir(), out}); err != nil {
		t.Fatal(err)
	}
	if files := readTree(t, out); len(files) != 0 {
		t.Errorf("wrote %v without a storage section", files)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestGenerateRefusesSharedMountPoint(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "storage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	section := `{
	  "disks": {"system": {"ref": "/dev/vda", "seed": "2869f04c-5655-50f4-28b9-6b2eb9700a02", "repart": {}}},
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": "2G"},
	    "one": {"disk": "system", "label": "one", "format": "ext4", "mountPoint": "/srv/data", "encryption": "none", "size": "1G"},
	    "two": {"disk": "system", "label": "two", "format": "ext4", "mountPoint": "/srv/data", "encryption": "none", "size": "1G"}
	  },
	  "fallback": "recovery-key"
	}`
	pins := `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"},
	  "partitions": {"one": "1b9a3f0e-2c4d-4e5f-8a6b-7c8d9e0f1a2b", "two": "2c0b4e1f-3d5e-4f60-9b7c-8d9eaf102b3c"}}}}`
	for name, content := range map[string]string{"storage.json": section, "disks.json": pins} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := t.TempDir()
	err := runGenerate([]string{"--state", state, out})
	if err == nil || !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("err = %v, want both volumes named", err)
	}
	if files := readTree(t, out); len(files) != 0 {
		t.Errorf("wrote %v for a section with a shared mount point", files)
	}
}
