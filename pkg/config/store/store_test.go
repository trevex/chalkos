package store

import (
	"bytes"
	"encoding/hex"
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
kind: Host
metadata: {name: default}
spec: {hostname: n1}
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

// stored is a config slot of set: its Demo as delivered, the same with a default filled in, and
// an identity.
func stored(t *testing.T, version string) Stored {
	t.Helper()
	delivered := parse(t, set)
	filled := parse(t, set)
	spec := filled[1].Object["spec"].(map[string]any)
	spec["mode"] = "safe"
	return Stored{Delivered: delivered, Documents: filled, Version: version, Identity: []byte(`{"hostname":"n1"}` + "\n")}
}

func TestWriteAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	in := stored(t, "v1")
	if err := Write(dir, in); err != nil {
		t.Fatal(err)
	}
	got, found, err := Read(dir)
	if err != nil || !found {
		t.Fatalf("read: %v, found %v", err, found)
	}
	want := in
	want.Delivered, want.Documents = slices.Clone(in.Delivered), slices.Clone(in.Documents)
	config.SortDocuments(want.Delivered)
	config.SortDocuments(want.Documents)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read %+v, want %+v", got, want)
	}
	if v, err := Version(dir); err != nil || v != "v1" {
		t.Errorf("version = %q, %v", v, err)
	}
	if target, err := os.Readlink(dir); err != nil || filepath.IsAbs(target) {
		t.Errorf("link = %q, %v; want a relative link", target, err)
	}
	for path, want := range map[string]os.FileMode{
		"documents/chalkos.dev/Host/default.json": 0o644,
		"delivered/chalkos.dev/Host/default.json": 0o644,
		"documents/secrets":                       0o700,
		"documents/secrets/demo.json":             0o600,
		"delivered/secrets":                       0o700,
		"delivered/secrets/demo.json":             0o600,
		"identity.json":                           0o600,
	} {
		if m := mode(t, filepath.Join(dir, path)); m != want {
			t.Errorf("%s mode = %v, want %v", path, m, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "documents", "chalkos.dev", "Secret")); !os.IsNotExist(err) {
		t.Error("stored the Secret beside the other documents")
	}
	if data := readFile(t, filepath.Join(dir, "delivered", "demo.example.org", "Demo", "one.json")); strings.Contains(data, "safe") {
		t.Errorf("the delivered Demo holds the default: %s", data)
	}
}

func TestWriteReplacesTheWholeSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := Write(dir, stored(t, "v1")); err != nil {
		t.Fatal(err)
	}
	cluster := parse(t, set)[:1]
	if err := Write(dir, Stored{Delivered: cluster, Documents: cluster, Version: "v2", Identity: []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	got, _, err := Read(dir)
	if err != nil || !reflect.DeepEqual(got.Documents, cluster) || !reflect.DeepEqual(got.Delivered, cluster) || got.Version != "v2" || string(got.Identity) != "{}\n" {
		t.Errorf("read %+v, %v", got, err)
	}
	for _, tree := range []string{"documents", "delivered"} {
		if _, err := os.Stat(filepath.Join(dir, tree, "demo.example.org")); !os.IsNotExist(err) {
			t.Errorf("kept a document of the replaced set in %s/", tree)
		}
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

func TestRefusesADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := dir + " is a directory, not a config link"
	if err := Write(dir, Stored{Documents: parse(t, set), Version: "v1"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("write: %v", err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		t.Errorf("the directory changed: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("left %v", l)
	}
}

// TestSlots keeps a slot per image below the directory of slots, each named by the image's root
// hash and replaced on its own, and lists them.
func TestSlots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	a, b := bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32)
	if got := Slot(root, a); got != filepath.Join(root, strings.Repeat("aa", 32)) {
		t.Errorf("slot = %s", got)
	}
	if hashes, err := Slots(root); err != nil || hashes != nil {
		t.Errorf("slots without a directory of slots = %x, %v", hashes, err)
	}
	for _, h := range [][]byte{b, a} {
		if err := Write(Slot(root, h), stored(t, "v-"+hex.EncodeToString(h[:1]))); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(Slot(root, a), stored(t, "v2")); err != nil {
		t.Fatal(err)
	}
	hashes, err := Slots(root)
	if err != nil || !reflect.DeepEqual(hashes, [][]byte{a, b}) {
		t.Errorf("slots = %x, %v; want aa… and bb…", hashes, err)
	}
	for h, want := range map[string][]byte{"v2": a, "v-bb": b} {
		if v, err := Version(Slot(root, want)); err != nil || v != h {
			t.Errorf("version of %x… = %q, %v; want %q", want[:2], v, err, h)
		}
	}
	if l := leftovers(t, Slot(root, a)); len(l) != 1 {
		t.Errorf("directories of slot aa…: %v, want the current one alone", l)
	}
}

// TestRetire removes every slot but those kept, with the directories its link named and those
// interrupted writes left, and keeps the kept ones whole.
func TestRetire(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	a, b, c := bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32), bytes.Repeat([]byte{0xcc}, 32)
	for _, h := range [][]byte{a, b} {
		if err := Write(Slot(root, h), stored(t, "v1")); err != nil {
			t.Fatal(err)
		}
	}
	interrupted(t, Slot(root, b))
	// A first write of c cut short leaves its directory without a link.
	interrupted(t, Slot(root, c))
	if err := Retire(root, [][]byte{a}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[1] != strings.Repeat("aa", 32) || !strings.HasPrefix(names[0], "."+strings.Repeat("aa", 32)+"-") {
		t.Errorf("left %v, want slot aa… alone", names)
	}
	if got, found, err := Read(Slot(root, a)); err != nil || !found || got.Version != "v1" {
		t.Errorf("the kept slot: %+v, found %v, %v", got, found, err)
	}
	if err := Retire(filepath.Join(t.TempDir(), "none"), nil); err != nil {
		t.Errorf("retire without a directory of slots: %v", err)
	}
}

// TestRetireRefusesADirectory leaves a directory in place of a slot's link alone and says so.
func TestRetireRefusesADirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	dir := Slot(root, bytes.Repeat([]byte{0xbb}, 32))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Retire(root, nil); err == nil || !strings.Contains(err.Error(), "is a directory, not a config link") {
		t.Errorf("retire: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory went: %v", err)
	}
}

// TestReadRuntime reads the documents WriteRuntime wrote for the node's services, Secrets
// excepted, and none from a directory without them.
func TestReadRuntime(t *testing.T) {
	dir := t.TempDir()
	if docs, err := ReadRuntime(dir); err != nil || docs != nil {
		t.Errorf("without documents: %v, %v", docs, err)
	}
	docs, err := config.Parse(strings.NewReader("apiVersion: oci.chalkos.dev/v1alpha1\nkind: Registry\nmetadata: {name: lan}\nspec: {host: \"10.0.0.5:5000\", insecure: true}\n---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: s}\nspec: {data: {k: v}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntime(dir, docs, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntime(dir)
	if err != nil || len(got) != 1 || got[0].Kind != "Registry" || got[0].Name != "lan" {
		t.Errorf("ReadRuntime = %v, %v; want the Registry alone", got, err)
	}
}
