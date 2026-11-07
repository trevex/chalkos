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

	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storage/hostops/hostopstest"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
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
	dir, err := s.Resolve(s.Toplevel + "/etc/chalkos/schemas")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
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

// fakeStore has the node open and mount the store an upgrade checks on a fake host, each mount
// laying the store out anew as writeStore does. Its runner records what the host ran.
func (l *lab) fakeStore() (*hostopstest.Fake, *tooltest.Runner) {
	r := &tooltest.Runner{}
	f := hostopstest.New(r)
	f.OnMount = func(_, dir, _ string) error {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		writeStore(l.t, dir)
		return nil
	}
	l.node.Ops = f
	return f, r
}

// failMounts makes every mount on the fake host fail.
func failMounts(r *tooltest.Runner) {
	r.Rules = []tooltest.Rule{{Prefix: "mount ", Err: errors.New("wrong fs type")}}
}

// TestTheImageIsCheckedBeforeItBoots: the caller checks the written store, opened through
// dm-verity with the image's root hash, before the image becomes the next boot. A refusal leaves
// the running image the next boot and nothing of the image on the ESP; an accepted image is
// activated, and the store is closed either way.
func TestTheImageIsCheckedBeforeItBoots(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	mount := filepath.Join(t.TempDir(), "store")
	_, r := l.fakeStore()
	l.node.StoreMount = mount
	var toplevel string
	l.node.CheckStore = func(s Store) error {
		toplevel = s.Toplevel
		if _, err := s.Resolve(s.Toplevel + "/../x/etc/chalkos/schemas"); err != nil {
			return err
		}
		return errors.New("the config does not suit the image")
	}
	_, err := l.install(img)
	if err == nil || !strings.Contains(err.Error(), "does not suit") {
		t.Fatalf("install = %v, want the check's refusal", err)
	}
	// The image was written, not found staged.
	if _, ok := errors.AsType[*StagedRetiredError](err); ok {
		t.Errorf("install = %v, a StagedRetiredError", err)
	}
	if _, ok := errors.AsType[*CheckError](err); !ok {
		t.Errorf("install = %v, want a CheckError", err)
	}
	if toplevel != "/nix/store/x" {
		t.Errorf("toplevel = %q, want the UKI's init= directory", toplevel)
	}
	parts := l.table()
	open := "veritysetup open " + parts[5].Node + " " + StoreMapping + " " + parts[4].Node + " " + hex.EncodeToString(img.root)
	if !slices.Contains(r.Calls, open) || !slices.Contains(r.Calls, "mount -t erofs -o ro /dev/mapper/"+StoreMapping+" "+mount) ||
		r.Calls[len(r.Calls)-1] != "veritysetup close "+StoreMapping {
		t.Errorf("ran %q", r.Calls)
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
	f, r := l.fakeStore()
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	f.SetMapped(StoreMapping, l.disk+"5")
	f.SetMounted(hostops.Mapper(StoreMapping), l.node.StoreMount)
	l.node.CheckStore = func(Store) error { return nil }
	partitions := l.node.OpenPartition
	l.node.OpenPartition = func(p gpt.Partition, write bool) (storeslot.PartitionFile, error) {
		if write && f.Mapped(StoreMapping) {
			return nil, errors.New("device or resource busy")
		}
		return partitions(p, write)
	}
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.boots(img, 2)
	if len(r.Calls) < 2 || r.Calls[0] != "umount "+l.node.StoreMount || r.Calls[1] != "veritysetup close "+StoreMapping {
		t.Errorf("ran %q, want the store closed first", r.Calls)
	}
}

// TestALeftOpenStoreThatStaysBusyStopsTheUpgrade: a left-open store that does not unmount stops
// the upgrade before it writes the slot, with the unmount's error; the next upgrade releases it
// and goes on.
func TestALeftOpenStoreThatStaysBusyStopsTheUpgrade(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	f, r := l.fakeStore()
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	f.SetMapped(StoreMapping, l.disk+"5")
	f.SetMounted(hostops.Mapper(StoreMapping), l.node.StoreMount)
	l.node.CheckStore = func(Store) error { return nil }
	r.Rules = []tooltest.Rule{{Prefix: "umount", Err: errors.New("target is busy"), Times: 1}}
	if _, err := l.install(img); err == nil || !strings.Contains(err.Error(), "unmount the checked store: target is busy") {
		t.Fatalf("install = %v, want the unmount's error", err)
	}
	if !slices.Equal(r.Calls, []string{"umount " + l.node.StoreMount}) {
		t.Errorf("ran %q, want only the unmount", r.Calls)
	}
	if e, name := l.boot(); name != old.name {
		t.Errorf("the disk boots %s from %s", name, e.File)
	}

	r.ResetCalls()
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.boots(img, 2)
	if len(r.Calls) < 3 || r.Calls[0] != "umount "+l.node.StoreMount || r.Calls[1] != "veritysetup close "+StoreMapping ||
		!strings.HasPrefix(r.Calls[2], "veritysetup open ") {
		t.Errorf("ran %q, want the store released once and then opened for the check", r.Calls)
	}
}

// TestAStoreThatDoesNotMountIsClosed:when the opened store does not mount, its dm-verity device
// is closed, and nothing is unmounted.
func TestAStoreThatDoesNotMountIsClosed(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	_, r := l.fakeStore()
	failMounts(r)
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	l.node.CheckStore = func(Store) error { t.Error("checked a store that did not mount"); return nil }
	if _, err := l.install(img); err == nil || !strings.Contains(err.Error(), "mount the written store") {
		t.Fatalf("install = %v", err)
	}
	i := slices.IndexFunc(r.Calls, func(c string) bool { return strings.HasPrefix(c, "mount ") })
	if i < 0 || !slices.Equal(r.Calls[i+1:], []string{"veritysetup close " + StoreMapping}) {
		t.Errorf("ran %q, want the device closed and nothing unmounted after the mount", r.Calls)
	}
	if e, name := l.boot(); name != old.name {
		t.Errorf("the disk boots %s from %s", name, e.File)
	}
}

// TestAStagedStoreThatDoesNotMountIsNoRefusal: a recheck of the staged image whose store does not
// mount retires the image as a refusal does, but its error holds no CheckError: the check never
// ran, so the image's config was not refused.
func TestAStagedStoreThatDoesNotMountIsNoRefusal(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	_, r := l.fakeStore()
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	checks := 0
	l.node.CheckStore = func(Store) error { checks++; return nil }
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	failMounts(r)
	_, err := l.install(img)
	var retired *StagedRetiredError
	var check *CheckError
	if !errors.As(err, &retired) || errors.As(err, &check) || !strings.Contains(err.Error(), "mount the written store") || checks != 1 {
		t.Fatalf("install = %v after %d checks, want a StagedRetiredError of the mount without a CheckError", err, checks)
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
	_, r := l.fakeStore()
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

	var check *CheckError
	if _, err := l.install(img); !errors.Is(err, refusal) || !errors.As(err, &retired) || !errors.As(retired.Err, &check) {
		t.Fatalf("install = %v, want the check's refusal, which retired the staged image", err)
	}
	if e, name := l.boot(); name != old.name || l.file(res.Entry) != nil {
		t.Errorf("after a refusal the disk boots %s from %s, and %s stayed: %v", name, e.File, res.Entry, l.file(res.Entry) != nil)
	}
	if parts := l.table(); parts[5].Name != "_empty" {
		t.Errorf("after a refusal the slot is labelled %q", parts[5].Name)
	}
	if r.Calls[len(r.Calls)-1] != "veritysetup close "+StoreMapping {
		t.Errorf("ran %q, want the store closed", r.Calls)
	}
}

// TestReadImage reads the store of an image the inactive slot holds, as a check of a staged
// config does after the upgrade: through dm-verity, with the system the image's UKI names, closed
// again afterwards. The running image and one the slot does not hold are refused.
func TestReadImage(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	_, r := l.fakeStore()
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	l.node.CheckStore = func(Store) error { return nil }
	if _, err := l.node.Install(context.Background(), img.header, img.stream()); err != nil {
		t.Fatal(err)
	}
	// The image is read while it is staged, through its staged UKI, and once it is active.
	for _, state := range []string{"staged", "active"} {
		if state == "active" {
			if _, err := l.node.Activate(context.Background(), img.root); err != nil {
				t.Fatal(err)
			}
		}
		r.ResetCalls()
		var toplevel string
		if err := l.node.ReadImage(context.Background(), img.root, func(s Store) error {
			toplevel = s.Toplevel
			_, err := s.Resolve(s.Toplevel + "/../x/etc/chalkos/schemas")
			return err
		}); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		parts := l.table()
		open := "veritysetup open " + parts[5].Node + " " + StoreMapping + " " + parts[4].Node + " " + hex.EncodeToString(img.root)
		if toplevel != "/nix/store/x" || !slices.Contains(r.Calls, open) || r.Calls[len(r.Calls)-1] != "veritysetup close "+StoreMapping {
			t.Errorf("%s: toplevel %q, ran %q", state, toplevel, r.Calls)
		}
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
	l.fakeStore()
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
	l.setUUID(5, gpt.NewUUID())
	if _, err := l.install(img); !errors.Is(err, refusal) || errors.As(err, &retired) {
		t.Errorf("install into a changed slot = %v, want the check's refusal without a StagedRetiredError", err)
	}
}

// TestARecheckRetiresAStagedImage: an image staged and not activated is checked again when it is
// installed again, and stays staged once the check passes; a refusal retires it with its staged
// UKI, and the disk boots the running image.
func TestARecheckRetiresAStagedImage(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	l.fakeStore()
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	var refusal error
	checks := 0
	l.node.CheckStore = func(Store) error { checks++; return refusal }
	stage := func() (Result, error) { return l.node.Install(context.Background(), img.header, img.stream()) }
	if _, err := stage(); err != nil {
		t.Fatal(err)
	}
	if res, err := stage(); err != nil || !res.AlreadyStaged || res.Active || checks != 2 {
		t.Fatalf("stage again = %+v, %v after %d checks; want it staged and checked again", res, err, checks)
	}
	refusal = errors.New("the config does not suit the image")
	var retired *StagedRetiredError
	if _, err := stage(); !errors.Is(err, refusal) || !errors.As(err, &retired) {
		t.Fatalf("stage = %v, want the check's refusal, which retired the staged image", err)
	}
	if files, err := esp.StagedFiles(l.esp); err != nil || len(files) != 0 {
		t.Errorf("staged UKIs after the refusal = %q, %v; want none", files, err)
	}
	if e, name := l.boot(); name != old.name {
		t.Errorf("after a refusal the disk boots %s from %s", name, e.File)
	}
	if parts := l.table(); parts[5].Name != "_empty" {
		t.Errorf("after a refusal the slot is labelled %q", parts[5].Name)
	}
}
