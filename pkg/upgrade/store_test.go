package upgrade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage/node"
)

// writeStore lays out an image's store as its erofs holds it under root: the system x, whose
// etc links into the store with an absolute link and a relative one, as NixOS's etc does.
func writeStore(t *testing.T, root string) {
	t.Helper()
	store := filepath.Join(root, "nix", "store")
	for _, dir := range []string{"x/etc/chalkos", "y-schemas", "z-image"} {
		if err := os.MkdirAll(filepath.Join(store, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"y-schemas/a.json": "{}", "z-image/image.json": "facts"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(store, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"x/etc/chalkos/schemas": "/nix/store/y-schemas",
		"x/etc/chalkos/image":   "../../../z-image",
	} {
		if err := os.Symlink(target, filepath.Join(store, link)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStoreFollowsTheImagesLinks: a path of the image resolves within the mounted store, its
// absolute links naming the image's paths, never the running system's.
func TestStoreFollowsTheImagesLinks(t *testing.T) {
	root := t.TempDir()
	writeStore(t, root)
	s := Store{root: root, Toplevel: "/nix/store/x"}
	entries, err := s.ReadDir(s.Toplevel + "/etc/chalkos/schemas")
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.json" {
		t.Errorf("schemas = %v, %v", entries, err)
	}
	if data, err := s.ReadFile(s.Toplevel + "/etc/chalkos/image/image.json"); err != nil || string(data) != "facts" {
		t.Errorf("image.json = %q, %v", data, err)
	}
	if _, err := s.ReadFile("/etc/os-release"); err == nil {
		t.Error("read a file outside the image's store")
	}
	if err := os.Symlink("loop", filepath.Join(root, "nix", "store", "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile("/nix/store/loop"); err == nil || !strings.Contains(err.Error(), "too many symbolic links") {
		t.Errorf("a link to itself: %v", err)
	}
}

// storeRunner runs commands as the runner it wraps, but opens and mounts the store an upgrade
// checks as writeStore lays it out, recording what it ran.
type storeRunner struct {
	node.Runner
	calls   *[]string
	mounted func(dir string)
	// open, when set, tells whether the store's dm-verity device is open, as veritysetup leaves
	// it; mountErr fails the mount.
	open     *bool
	mountErr error
}

func (r storeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "veritysetup", "umount":
		*r.calls = append(*r.calls, name+" "+strings.Join(args, " "))
		if r.open != nil && name == "veritysetup" {
			*r.open = args[0] == "open"
		}
		return nil, nil
	case "mount":
		*r.calls = append(*r.calls, name+" "+strings.Join(args, " "))
		if r.mountErr != nil {
			return nil, r.mountErr
		}
		r.mounted(args[len(args)-1])
		return nil, nil
	}
	return r.Runner.Run(ctx, name, args...)
}

// TestTheImageIsCheckedBeforeItBoots: the caller checks the written store, opened through
// dm-verity with the image's root hash, before the image becomes the next boot. A refusal leaves
// the running image the next boot and nothing of the image on the ESP; an accepted image is
// activated, and the store is closed either way.
func TestTheImageIsCheckedBeforeItBoots(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	mount := filepath.Join(t.TempDir(), "store")
	var calls []string
	l.node.Run = storeRunner{Runner: l.node.Run, calls: &calls, mounted: func(dir string) { writeStore(t, dir) }}
	l.node.StoreMount = mount
	var toplevel string
	l.node.CheckStore = func(s Store) error {
		toplevel = s.Toplevel
		if _, err := s.ReadDir(s.Toplevel + "/../x/etc/chalkos/schemas"); err != nil {
			return err
		}
		return errors.New("the config does not suit the image")
	}
	_, err := l.install(img)
	if err == nil || !strings.Contains(err.Error(), "does not suit") {
		t.Fatalf("install = %v, want the check's refusal", err)
	}
	// The image was written, not found staged.
	var retired *StagedRetiredError
	if errors.As(err, &retired) {
		t.Errorf("install = %v, a StagedRetiredError", err)
	}
	if toplevel != "/nix/store/x" {
		t.Errorf("toplevel = %q, want the UKI's init= directory", toplevel)
	}
	parts := l.table()
	open := "veritysetup open " + parts[5].Node + " " + storeMapping + " " + parts[4].Node + " " + hex.EncodeToString(img.root)
	if !slices.Contains(calls, open) || !slices.Contains(calls, "mount -t erofs -o ro /dev/mapper/"+storeMapping+" "+mount) ||
		calls[len(calls)-1] != "veritysetup close "+storeMapping {
		t.Errorf("ran %q", calls)
	}
	if e, name := l.boot(); name != old.name {
		t.Errorf("after a refusal the disk boots %s from %s", name, e.File)
	}
	if temp := l.tempUKIs(); len(temp) != 0 || parts[5].Name != "_empty" {
		t.Errorf("after a refusal the ESP holds %v and the slot is labelled %q", temp, parts[5].Name)
	}

	if err := os.RemoveAll(mount); err != nil {
		t.Fatal(err)
	}
	l.node.CheckStore = func(Store) error { return nil }
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.boots(img, 2)
}

// TestALeftOpenStoreIsClosedFirst: a store an earlier check left open, as when chalkd was stopped
// during it, holds the slot's partitions; the next upgrade closes it before it writes the slot.
func TestALeftOpenStoreIsClosedFirst(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	var calls []string
	open := true
	l.node.Run = storeRunner{Runner: l.node.Run, calls: &calls, mounted: func(dir string) { writeStore(t, dir) }, open: &open}
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	l.node.CheckStore = func(Store) error { return nil }
	partitions := l.node.OpenPartition
	l.node.OpenPartition = func(p Partition, write bool) (PartitionFile, error) {
		if write && open {
			return nil, errors.New("device or resource busy")
		}
		return partitions(p, write)
	}
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.boots(img, 2)
	if len(calls) < 2 || calls[0] != "umount "+l.node.StoreMount || calls[1] != "veritysetup close "+storeMapping {
		t.Errorf("ran %q, want the store closed first", calls)
	}
}

// TestAStoreThatDoesNotMountIsClosed: when the opened store does not mount, its dm-verity device
// is closed, and nothing is unmounted.
func TestAStoreThatDoesNotMountIsClosed(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	var calls []string
	l.node.Run = storeRunner{Runner: l.node.Run, calls: &calls, mountErr: errors.New("wrong fs type")}
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	l.node.CheckStore = func(Store) error { t.Error("checked a store that did not mount"); return nil }
	if _, err := l.install(img); err == nil || !strings.Contains(err.Error(), "mount the written store") {
		t.Fatalf("install = %v", err)
	}
	i := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "mount ") })
	if i < 0 || !slices.Equal(calls[i+1:], []string{"veritysetup close " + storeMapping}) {
		t.Errorf("ran %q, want the device closed and nothing unmounted after the mount", calls)
	}
	if e, name := l.boot(); name != old.name {
		t.Errorf("the disk boots %s from %s", name, e.File)
	}
}

func TestToplevel(t *testing.T) {
	for cmdline, want := range map[string]string{
		"init=/nix/store/x/init usrhash=ab":                   "/nix/store/x",
		"init=/nix/store/a/init quiet init=/nix/store/b/init": "/nix/store/b",
		"init=/nix/store/a/init -- init=/nix/store/b/init":    "/nix/store/a",
		"quiet usrhash=ab":          "",
		"init=init":                 "",
		"init=":                     "",
		"-- init=/nix/store/b/init": "",
	} {
		got, err := toplevel(cmdline)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("toplevel(%q) = %q, %v; want %q", cmdline, got, err, want)
		}
	}
}

// TestAStagedImageIsCheckedAgain: installing the image that boots next already checks it again,
// as the config may have changed since. Accepted, nothing changes; refused, the slot and the entry
// are retired, and the running image boots next.
func TestAStagedImageIsCheckedAgain(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	var calls []string
	l.node.Run = storeRunner{Runner: l.node.Run, calls: &calls, mounted: func(dir string) {
		// Each check mounts the store anew.
		os.RemoveAll(dir)
		writeStore(t, dir)
	}}
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	checks := 0
	var refusal error
	l.node.CheckStore = func(s Store) error {
		checks++
		if s.Toplevel != "/nix/store/x" {
			t.Errorf("toplevel = %q, want the staged UKI's init= directory", s.Toplevel)
		}
		return refusal
	}
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.writes = map[int]int{}
	res, err := l.install(img)
	if err != nil || !res.AlreadyStaged || checks != 2 || len(l.writes) != 0 {
		t.Fatalf("install again = %+v, %v after %d checks, writes %v; want it staged and checked again", res, err, checks, l.writes)
	}
	l.boots(img, 2)

	refusal = errors.New("the config does not suit the image")
	// A refusal whose retirement fails says the image still boots next.
	l.node.Change = func(what string) error {
		if strings.HasPrefix(what, "remove ") {
			return errCrash
		}
		return nil
	}
	var retired *StagedRetiredError
	if _, err := l.install(img); !errors.Is(err, refusal) || !errors.Is(err, errCrash) || !strings.Contains(err.Error(), "which still boots next") || errors.As(err, &retired) {
		t.Fatalf("install = %v, want the refusal and the failed retirement", err)
	}
	l.boots(img, 2)
	l.node.Change = nil

	if _, err := l.install(img); !errors.Is(err, refusal) || !errors.As(err, &retired) {
		t.Fatalf("install = %v, want the check's refusal, which retired the staged image", err)
	}
	if e, name := l.boot(); name != old.name || l.file(res.Entry) != nil {
		t.Errorf("after a refusal the disk boots %s from %s, and %s stayed: %v", name, e.File, res.Entry, l.file(res.Entry) != nil)
	}
	if parts := l.table(); parts[5].Name != "_empty" {
		t.Errorf("after a refusal the slot is labelled %q", parts[5].Name)
	}
	if calls[len(calls)-1] != "veritysetup close "+storeMapping {
		t.Errorf("ran %q, want the store closed", calls)
	}
}

// TestReadImage reads the store of an image the inactive slot holds, as a check of a staged
// config does after the upgrade: through dm-verity, with the system the image's UKI names, closed
// again afterwards. The running image and one the slot does not hold are refused.
func TestReadImage(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	var calls []string
	l.node.Run = storeRunner{Runner: l.node.Run, calls: &calls, mounted: func(dir string) {
		os.RemoveAll(dir)
		writeStore(t, dir)
	}}
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	l.node.CheckStore = func(Store) error { return nil }
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	calls = nil
	var toplevel string
	if err := l.node.ReadImage(context.Background(), img.root, func(s Store) error {
		toplevel = s.Toplevel
		_, err := s.ReadDir(s.Toplevel + "/../x/etc/chalkos/schemas")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	parts := l.table()
	open := "veritysetup open " + parts[5].Node + " " + storeMapping + " " + parts[4].Node + " " + hex.EncodeToString(img.root)
	if toplevel != "/nix/store/x" || !slices.Contains(calls, open) || calls[len(calls)-1] != "veritysetup close "+storeMapping {
		t.Errorf("toplevel %q, ran %q", toplevel, calls)
	}
	for name, root := range map[string][]byte{"the running image": old.root, "another image": bytes.Repeat([]byte{0xee}, 32)} {
		err := l.node.ReadImage(context.Background(), root, func(Store) error { t.Errorf("%s: read", name); return nil })
		if err == nil {
			t.Errorf("%s: read without an error", name)
		}
	}
}

// TestOnlyARecheckRetiresTheStagedImage: an install of the staged image that fails before the
// recheck, or whose slot no longer holds the image so it is written anew, returns no
// StagedRetiredError, though the image may be gone after the second; the caller only says the
// staged image was retired with its refused config when the recheck retired it.
func TestOnlyARecheckRetiresTheStagedImage(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	l.node.Run = storeRunner{Runner: l.node.Run, calls: &[]string{}, mounted: func(dir string) {
		os.RemoveAll(dir)
		writeStore(t, dir)
	}}
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	var refusal error
	l.node.CheckStore = func(Store) error { return refusal }
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	refusal = errors.New("the config does not suit the image")
	var retired *StagedRetiredError

	other := img
	other.header.Architecture = "arm64"
	if _, err := l.node.Install(context.Background(), other.header, other.stream()); err == nil || errors.As(err, &retired) {
		t.Errorf("install for another architecture = %v, want a refusal that retired nothing", err)
	}
	l.boots(img, 2)

	// Another program changed the slot's UUIDs, so the image is written anew and checked then.
	l.setUUID(5, randomUUID())
	if _, err := l.install(img); !errors.Is(err, refusal) || errors.As(err, &retired) {
		t.Errorf("install into a changed slot = %v, want the check's refusal without a StagedRetiredError", err)
	}
}
