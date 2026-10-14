package store

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

const set = `
apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec: {endpoint: "https://10.0.0.1:6443"}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first, replicas: 3, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

func parse(t *testing.T, yaml string) []config.Document {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// leftovers lists the directories of earlier or interrupted writes next to dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+"-*"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestWriteAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	in := parse(t, set)
	if err := Write(dir, Stored{Documents: in, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := Read(dir)
	if err != nil || !found {
		t.Fatalf("read: %v, found %v", err, found)
	}
	want := slices.Clone(in)
	config.SortDocuments(want)
	if !reflect.DeepEqual(got.Documents, want) || got.Version != "v1" {
		t.Errorf("read %+v, want %+v", got, Stored{Documents: want, Version: "v1"})
	}
	if v, err := Version(dir); err != nil || v != "v1" {
		t.Errorf("version = %q, %v", v, err)
	}
	if target, err := os.Readlink(dir); err != nil || filepath.IsAbs(target) {
		t.Errorf("link = %q, %v; want a relative link", target, err)
	}
	if m := mode(t, filepath.Join(dir, "chalkos.dev", "Cluster", "lab.json")); m != 0o644 {
		t.Errorf("Cluster mode = %v", m)
	}
	if m := mode(t, filepath.Join(dir, "secrets")); m != 0o700 {
		t.Errorf("secrets/ mode = %v", m)
	}
	if m := mode(t, filepath.Join(dir, "secrets", "demo.json")); m != 0o600 {
		t.Errorf("Secret mode = %v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "chalkos.dev", "Secret")); !os.IsNotExist(err) {
		t.Error("stored the Secret beside the other documents")
	}
}

func TestWriteReplacesTheWholeSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := Write(dir, Stored{Documents: parse(t, set), Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	cluster := parse(t, set)[:1]
	if err := Write(dir, Stored{Documents: cluster, Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := Read(dir)
	if err != nil || !reflect.DeepEqual(got.Documents, cluster) || got.Version != "v2" {
		t.Errorf("read %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo.example.org")); !os.IsNotExist(err) {
		t.Error("kept a document of the replaced set")
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Errorf("directories next to the set: %v, want the current one alone", l)
	}
}

func TestReadWithoutSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if s, found, err := Read(dir); err != nil || found || s.Documents != nil {
		t.Errorf("read %+v, found %v, %v", s, found, err)
	}
	if v, err := Version(dir); err != nil || v != "" {
		t.Errorf("version = %q, %v", v, err)
	}
}

func TestRemove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := Write(dir, Stored{Documents: parse(t, set), Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the set remains: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("left %v", l)
	}
	if err := Remove(dir); err != nil {
		t.Errorf("removing no set: %v", err)
	}
}

func TestWriteRefusesANameThatIsNoFileName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	d := config.FromObject(map[string]any{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": map[string]any{"name": "../escape"}})
	if err := Write(dir, Stored{Documents: []config.Document{d}}); err == nil || !strings.Contains(err.Error(), "../escape") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Error("stored a refused set")
	}
}

// interrupted leaves a directory next to dir as a write cut short would, holding a document.
func interrupted(t *testing.T, dir string) string {
	t.Helper()
	stale := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+"-stale")
	doc := filepath.Join(stale, "demo.example.org", "Demo", "stale.json")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	obj := `{"apiVersion":"demo.example.org/v1alpha1","kind":"Demo","metadata":{"name":"stale"},"spec":{"name":"stale"}}`
	if err := os.WriteFile(doc, []byte(obj), 0o644); err != nil {
		t.Fatal(err)
	}
	return stale
}

func TestReadIgnoresAnInterruptedWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := Write(dir, Stored{Documents: parse(t, set), Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	interrupted(t, dir)
	got, _, err := Read(dir)
	if err != nil || len(got.Documents) != len(parse(t, set)) {
		t.Errorf("read %+v, %v; want the stored set alone", got, err)
	}
}

func TestRemoveTakesAnInterruptedWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := Write(dir, Stored{Documents: parse(t, set), Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	interrupted(t, dir)
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("left %v", l)
	}
}

func TestRemoveRefusesALinkItDidNotWrite(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "config")
	other := filepath.Join(parent, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"other", other} {
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		if err := Remove(dir); err == nil {
			t.Errorf("removed a link to %s", target)
		}
		if _, err := os.Stat(other); err != nil {
			t.Errorf("removed %s: %v", other, err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRefusesADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := dir + " is a directory, not a config link"
	if err := Write(dir, Stored{Documents: parse(t, set), Version: "v1"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("write: %v", err)
	}
	if err := Remove(dir); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("remove: %v", err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		t.Errorf("the directory changed: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("left %v", l)
	}
}
