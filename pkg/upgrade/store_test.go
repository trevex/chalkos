package upgrade

import (
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
}

func (r storeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "veritysetup", "umount":
		*r.calls = append(*r.calls, name+" "+strings.Join(args, " "))
		return nil, nil
	case "mount":
		*r.calls = append(*r.calls, name+" "+strings.Join(args, " "))
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
	old, img := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 2)
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
	if toplevel != "/nix/store/x" {
		t.Errorf("toplevel = %q, want the UKI's init= directory", toplevel)
	}
	parts := l.table()
	open := "veritysetup open " + parts[5].Node + " " + storeMapping + " " + parts[4].Node + " " + hex.EncodeToString(img.root)
	if !slices.Contains(calls, open) || !slices.Contains(calls, "mount -t erofs -o ro /dev/mapper/"+storeMapping+" "+mount) ||
		calls[len(calls)-1] != "veritysetup close "+storeMapping {
		t.Errorf("ran %q", calls)
	}
	if e, version := l.boot(); version != old.version {
		t.Errorf("after a refusal the disk boots %s from %s", version, e.File)
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
