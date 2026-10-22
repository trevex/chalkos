package inventory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// write writes a file, making its directory.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// homelab is the spec's example inventory.
const homelab = `apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    node-metal: {source: {directory: images/node-metal}}
    node-gpu: {source: {directory: /srv/images/node-gpu}}
  all:
    imageName: node-metal
    files: [cluster/*.yaml]
  groups:
    gpu: {imageName: node-gpu, files: [gpu/*.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, groups: [gpu], files: [nodes/w1.yaml]}
`

// TestLoadMakesPathsAbsolute resolves every path of the inventory against its directory.
func TestLoadMakesPathsAbsolute(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), homelab)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Name != "homelab" || inv.Dir() != dir {
		t.Errorf("inventory = %+v", inv)
	}
	if got := inv.Spec.All.Files; !slices.Equal(got, []string{filepath.Join(dir, "cluster", "*.yaml")}) {
		t.Errorf("files = %q", got)
	}
	if got := inv.Spec.Images["node-metal"].Source.Directory; got != filepath.Join(dir, "images", "node-metal") {
		t.Errorf("the directory of node-metal = %s", got)
	}
	if got := inv.Spec.Groups["gpu"].Files; !slices.Equal(got, []string{filepath.Join(dir, "gpu", "*.yaml")}) {
		t.Errorf("gpu's files = %q", got)
	}
	w1 := inv.Spec.Nodes["w1"]
	if w1.Address != "10.0.0.21" || !slices.Equal(w1.Groups, []string{"gpu"}) || !slices.Equal(w1.Files, []string{filepath.Join(dir, "nodes", "w1.yaml")}) {
		t.Errorf("w1 = %+v", w1)
	}
	if got := inv.NodeNames(); !slices.Equal(got, []string{"cp1", "w1"}) {
		t.Errorf("nodes = %q", got)
	}
}

// TestLoadRefuses refuses an inventory its schema does not accept, and a node in a group the
// inventory does not define.
func TestLoadRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ content, want string }{
		"a misspelled field":  {strings.Replace(homelab, "address: 10.0.0.11", "adress: 10.0.0.11", 1), "adress"},
		"another kind":        {strings.Replace(homelab, "kind: Inventory", "kind: Lab", 1), `"Lab"`},
		"a node name in caps": {strings.Replace(homelab, "    cp1:", "    CP1:", 1), "CP1"},
		"an empty file list":  {strings.Replace(homelab, "files: [nodes/cp1.yaml]", "files: [\"\"]", 1), "nodes"},
		"an unknown group":    {strings.Replace(homelab, "groups: [gpu]", "groups: [gpus]", 1), "the group gpus, which the inventory does not define"},
		"two documents":       {homelab + "---\n" + homelab, "holds 2 documents"},
		"a secrets file":      {strings.Replace(homelab, "spec:\n", "spec:\n  secrets: ./secrets.age\n", 1), "secrets"},
		"a top-level image":   {strings.Replace(homelab, "spec:\n", "spec:\n  image: node-metal\n", 1), "image"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chalkos.yaml")
			write(t, path, tc.content)
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// TestFilesInOrder gives a node the shared files, then its groups', then its own, a glob's files
// in name order, and refuses a path or glob that names no file.
func TestFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), homelab)
	for _, f := range []string{"cluster/b.yaml", "cluster/a.yaml", "gpu/driver.yaml", "nodes/w1.yaml", "nodes/cp1.yaml"} {
		write(t, filepath.Join(dir, f), "")
	}
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := inv.Files("w1")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range []string{"cluster/a.yaml", "cluster/b.yaml", "gpu/driver.yaml", "nodes/w1.yaml"} {
		want = append(want, filepath.Join(dir, f))
	}
	if !slices.Equal(got, want) {
		t.Errorf("files of w1 = %q, want %q", got, want)
	}
	if _, err := inv.Files("w9"); err == nil || !strings.Contains(err.Error(), "has no node w9; its nodes are cp1, w1") {
		t.Errorf("files of w9 = %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "gpu", "driver.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Files("w1"); err == nil || !strings.Contains(err.Error(), "node w1: gpu/*.yaml names no file") {
		t.Errorf("files of w1 without gpu's = %v", err)
	}
}

// TestFilesOnce gives a node a file that several paths, globs or groups name once, where it is
// named first.
func TestFilesOnce(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), strings.Replace(homelab, "groups: [gpu], files: [nodes/w1.yaml]", "groups: [gpu, gpu], files: [nodes/w1.yaml, cluster/a.yaml, gpu/*.yaml]", 1))
	for _, f := range []string{"cluster/a.yaml", "cluster/b.yaml", "gpu/driver.yaml", "nodes/w1.yaml", "nodes/cp1.yaml"} {
		write(t, filepath.Join(dir, f), "")
	}
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := inv.Files("w1")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range []string{"cluster/a.yaml", "cluster/b.yaml", "gpu/driver.yaml", "nodes/w1.yaml"} {
		want = append(want, filepath.Join(dir, f))
	}
	if !slices.Equal(got, want) {
		t.Errorf("files of w1 = %q, want %q", got, want)
	}
}

// TestFilesBelowAGlobsDirectory reads the files of an inventory whose directory's name holds
// characters a glob reads as a pattern's, and an absolute glob as written.
func TestFilesBelowAGlobsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lab[1]*?")
	other := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), strings.Replace(homelab, "files: [nodes/w1.yaml]", "files: [nodes/w1.yaml, "+filepath.Join(other, "*.yaml")+"]", 1))
	for _, f := range []string{"cluster/a.yaml", "gpu/driver.yaml", "nodes/w1.yaml"} {
		write(t, filepath.Join(dir, f), "")
	}
	write(t, filepath.Join(other, "extra.yaml"), "")
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := inv.Files("w1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "cluster", "a.yaml"), filepath.Join(dir, "gpu", "driver.yaml"), filepath.Join(dir, "nodes", "w1.yaml"), filepath.Join(other, "extra.yaml")}
	if !slices.Equal(got, want) {
		t.Errorf("files of w1 = %q, want %q", got, want)
	}
	if _, err := inv.Files("cp1"); err == nil || !strings.Contains(err.Error(), "nodes/cp1.yaml names no file") {
		t.Errorf("files of cp1 = %v, want nodes/cp1.yaml named relative to the inventory", err)
	}
}

// TestFilesSkipDirectories leaves out the directories a glob matches, and refuses a path or glob
// that names directories alone.
func TestFilesSkipDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), strings.Replace(homelab, "files: [nodes/w1.yaml]", "files: [nodes/*]", 1))
	for _, f := range []string{"cluster/a.yaml", "gpu/driver.yaml", "nodes/w1.yaml", "nodes/old/w1.yaml"} {
		write(t, filepath.Join(dir, f), "")
	}
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := inv.Files("w1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "nodes", "w1.yaml"); got[len(got)-1] != want || slices.Contains(got, filepath.Join(dir, "nodes", "old")) {
		t.Errorf("files of w1 = %q, want %s last and no directory", got, want)
	}
	write(t, filepath.Join(dir, "chalkos.yaml"), strings.Replace(homelab, "files: [nodes/w1.yaml]", "files: [nodes/old]", 1))
	if inv, err = Load(filepath.Join(dir, "chalkos.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Files("w1"); err == nil || !strings.Contains(err.Error(), "nodes/old names only the directory nodes/old; name its files, such as nodes/old/*.yaml") {
		t.Errorf("files of w1 naming a directory = %v", err)
	}
}

// TestFilesNameTheGlobOfABrokenLink names the glob that matches a link whose target is gone.
func TestFilesNameTheGlobOfABrokenLink(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), homelab)
	for _, f := range []string{"cluster/a.yaml", "gpu/driver.yaml", "nodes/w1.yaml"} {
		write(t, filepath.Join(dir, f), "")
	}
	if err := os.Symlink(filepath.Join(dir, "gone.yaml"), filepath.Join(dir, "cluster", "b.yaml")); err != nil {
		t.Fatal(err)
	}
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Files("w1"); err == nil || !strings.Contains(err.Error(), "node w1: cluster/*.yaml: ") || !strings.Contains(err.Error(), "b.yaml") {
		t.Errorf("files of w1 with a broken link = %v", err)
	}
}
