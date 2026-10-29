package slot

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io/fs"
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
	cluster := config.Documents(parse(t, set)[:1])
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
	if got := Dir(root, a); got != filepath.Join(root, strings.Repeat("aa", 32)) {
		t.Errorf("slot = %s", got)
	}
	if hashes, err := List(root); err != nil || hashes != nil {
		t.Errorf("slots without a directory of slots = %x, %v", hashes, err)
	}
	for _, h := range [][]byte{b, a} {
		if err := Write(Dir(root, h), stored(t, "v-"+hex.EncodeToString(h[:1]))); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(Dir(root, a), stored(t, "v2")); err != nil {
		t.Fatal(err)
	}
	hashes, err := List(root)
	if err != nil || !reflect.DeepEqual(hashes, [][]byte{a, b}) {
		t.Errorf("slots = %x, %v; want aa… and bb…", hashes, err)
	}
	for h, want := range map[string][]byte{"v2": a, "v-bb": b} {
		if v, err := Version(Dir(root, want)); err != nil || v != h {
			t.Errorf("version of %x… = %q, %v; want %q", want[:2], v, err, h)
		}
	}
	if l := leftovers(t, Dir(root, a)); len(l) != 1 {
		t.Errorf("directories of slot aa…: %v, want the current one alone", l)
	}
}

// TestRetire removes every slot but those kept, with the directories its link named and those
// interrupted writes left, and keeps the kept ones whole.
func TestRetire(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	a, b, c := bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32), bytes.Repeat([]byte{0xcc}, 32)
	for _, h := range [][]byte{a, b} {
		if err := Write(Dir(root, h), stored(t, "v1")); err != nil {
			t.Fatal(err)
		}
	}
	interrupted(t, Dir(root, b))
	// A first write of c cut short leaves its directory without a link.
	interrupted(t, Dir(root, c))
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
	if got, found, err := Read(Dir(root, a)); err != nil || !found || got.Version != "v1" {
		t.Errorf("the kept slot: %+v, found %v, %v", got, found, err)
	}
	if err := Retire(filepath.Join(t.TempDir(), "none"), nil); err != nil {
		t.Errorf("retire without a directory of slots: %v", err)
	}
}

// TestRetireRefusesADirectory leaves a directory in place of a slot's link alone and says so.
func TestRetireRefusesADirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	dir := Dir(root, bytes.Repeat([]byte{0xbb}, 32))
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

// TestWriteKeepingPrevious keeps the config a write replaces as the slot's previous one, one level
// deep: the slot's directory before goes once a third write replaces the second. A write of the
// version the slot holds keeps the previous config as it is, and Write drops it.
func TestWriteKeepingPrevious(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := WriteKeepingPrevious(dir, stored(t, "v1")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := Read(Previous(dir)); err != nil || found {
		t.Fatalf("a first write keeps a previous config: found %v, %v", found, err)
	}
	if v, err := Version(Previous(dir)); err != nil || v != "" {
		t.Errorf("previous version without one = %q, %v", v, err)
	}
	for i, next := range []string{"v2", "v3"} {
		if err := WriteKeepingPrevious(dir, stored(t, next)); err != nil {
			t.Fatal(err)
		}
		want := []string{"v1", "v2"}[i]
		if got, found, err := Read(Previous(dir)); err != nil || !found || got.Version != want || len(got.Delivered) != 3 {
			t.Errorf("after %s the previous config is %+v, found %v, %v; want %s whole", next, got, found, err, want)
		}
		if l := leftovers(t, dir); len(l) != 2 {
			t.Errorf("after %s the directories next to the slot are %v, want the current one and the previous one", next, l)
		}
	}
	if target, err := os.Readlink(Previous(dir)); err != nil || filepath.IsAbs(target) {
		t.Errorf("previous link = %q, %v; want a relative link", target, err)
	}
	// The same version again: the previous config stays v2.
	if err := WriteKeepingPrevious(dir, stored(t, "v3")); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(Previous(dir)); v != "v2" {
		t.Errorf("after the same version again the previous config is %q, want v2", v)
	}
	if err := Write(dir, stored(t, "v4")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Previous(dir)); !os.IsNotExist(err) {
		t.Errorf("Write kept the previous config: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Errorf("after Write the directories next to the slot are %v, want the current one alone", l)
	}
}

// TestWriteKeepingPreviousRefusesADirectory leaves a directory where the previous config's link
// would be alone and says so.
func TestWriteKeepingPreviousRefusesADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(Previous(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingPrevious(dir, stored(t, "v1")); err == nil || !strings.Contains(err.Error(), "is a directory, not a config link") {
		t.Errorf("write: %v", err)
	}
}

// TestRetireRemovesThePreviousConfig removes a retired slot's previous config and keeps a kept
// slot's.
func TestRetireRemovesThePreviousConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	a, b := bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32)
	for _, h := range [][]byte{a, b} {
		for _, v := range []string{"v1", "v2"} {
			if err := WriteKeepingPrevious(Dir(root, h), stored(t, v)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Retire(root, [][]byte{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Previous(Dir(root, b))); !os.IsNotExist(err) {
		t.Errorf("the retired slot's previous config stayed: %v", err)
	}
	if l := leftovers(t, Dir(root, b)); len(l) != 0 {
		t.Errorf("the retired slot left %v", l)
	}
	if v, err := Version(Previous(Dir(root, a))); err != nil || v != "v1" {
		t.Errorf("the kept slot's previous config = %q, %v; want v1", v, err)
	}
}

// TestWriteKeepingPreviousWithoutASlot drops a previous config that outlived its slot, as an
// interrupted Retire may leave it, so a fresh slot never names another image's set.
func TestWriteKeepingPreviousWithoutASlot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := WriteKeepingPrevious(dir, stored(t, "v1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingPrevious(dir, stored(t, "v2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingPrevious(dir, stored(t, "v3")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Previous(dir)); !os.IsNotExist(err) {
		t.Errorf("the previous config of the slot before stayed: %v", err)
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Errorf("the directories next to the slot are %v, want the current one alone", l)
	}
}

// TestWriteKeepingPreviousThatFails keeps the slot and its previous config as they were when the
// new set cannot be written.
func TestWriteKeepingPreviousThatFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	for _, v := range []string{"v1", "v2"} {
		if err := WriteKeepingPrevious(dir, stored(t, v)); err != nil {
			t.Fatal(err)
		}
	}
	d := config.FromObject(map[string]any{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": map[string]any{"name": "../escape"}})
	if err := WriteKeepingPrevious(dir, Stored{Documents: []config.Document{d}, Version: "v3"}); err == nil {
		t.Fatal("wrote a refused set")
	}
	current, _ := Version(dir)
	previous, _ := Version(Previous(dir))
	if current != "v2" || previous != "v1" {
		t.Errorf("after a failed write the slot holds %q and its previous config %q, want v2 and v1", current, previous)
	}
	if l := leftovers(t, dir); len(l) != 2 {
		t.Errorf("the directories next to the slot are %v, want the current one and the previous one", l)
	}
}

// TestWriteKeepingPreviousThatFailsLate keeps the previous config when the slot's link cannot
// move after the previous link did: the previous link goes back to the set it named.
func TestWriteKeepingPreviousThatFailsLate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	for _, v := range []string{"v1", "v2"} {
		if err := WriteKeepingPrevious(dir, stored(t, v)); err != nil {
			t.Fatal(err)
		}
	}
	// A directory where replace puts the slot's new link cannot be removed.
	blocked := filepath.Join(filepath.Dir(dir), ".config.link")
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingPrevious(dir, stored(t, "v3")); err == nil {
		t.Fatal("wrote the set with its link blocked")
	}
	current, _ := Version(dir)
	previous, _ := Version(Previous(dir))
	if current != "v2" || previous != "v1" {
		t.Errorf("after a failed write the slot holds %q and its previous config %q, want v2 and v1", current, previous)
	}
}

// TestRestorePrevious makes the previous config the slot's again and the config it held the
// previous one, so a second restore undoes the first; a slot without a previous config, or whose
// previous link names the set it holds, as an interrupted write leaves it, is refused.
func TestRestorePrevious(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := WriteKeepingPrevious(dir, stored(t, "v1")); err != nil {
		t.Fatal(err)
	}
	if err := RestorePrevious(dir); err == nil || !strings.Contains(err.Error(), "keeps no previous config") {
		t.Errorf("without a previous config: %v", err)
	}
	if err := WriteKeepingPrevious(dir, stored(t, "v2")); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{{"v1", "v2"}, {"v2", "v1"}} {
		if err := RestorePrevious(dir); err != nil {
			t.Fatal(err)
		}
		running, _ := Version(dir)
		previous, _ := Version(Previous(dir))
		if running != want[0] || previous != want[1] {
			t.Errorf("after a restore the slot runs %q and keeps %q, want %q and %q", running, previous, want[0], want[1])
		}
		if got, found, err := Read(dir); err != nil || !found || len(got.Delivered) != 3 {
			t.Errorf("the restored set reads %+v, %v, %v; want it whole", got, found, err)
		}
		if l := leftovers(t, dir); len(l) != 2 {
			t.Errorf("the directories next to the slot are %v, want both sets", l)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), ".config.link")); !os.IsNotExist(err) {
			t.Errorf("a temporary link is left: %v", err)
		}
	}
	held, err := os.Readlink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := relink(Previous(dir), held); err != nil {
		t.Fatal(err)
	}
	if err := RestorePrevious(dir); err == nil || !strings.Contains(err.Error(), "keeps no previous config") {
		t.Errorf("with both links on one set: %v", err)
	}
	// A previous link whose set is gone is refused before the slot's link moves to it.
	if err := relink(Previous(dir), "missing"); err != nil {
		t.Fatal(err)
	}
	if err := RestorePrevious(dir); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), dir+"'s previous config missing is gone") {
		t.Errorf("with a dangling previous link: %v", err)
	}
	// One whose set cannot be read for another reason says so.
	loop := filepath.Base(Previous(dir))
	if err := relink(Previous(dir), loop); err != nil {
		t.Fatal(err)
	}
	if err := RestorePrevious(dir); err == nil || !strings.Contains(err.Error(), dir+"'s previous config "+loop+" cannot be read: ") {
		t.Errorf("with a previous link to itself: %v", err)
	}
	if now, err := os.Readlink(dir); err != nil || now != held {
		t.Errorf("after a refused restore the slot links to %q, %v; want %q", now, err, held)
	}
	// A slot that does not exist has nothing to restore.
	if err := RestorePrevious(filepath.Join(filepath.Dir(dir), "none")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a slot that does not exist: %v", err)
	}
}
