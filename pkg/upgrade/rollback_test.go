package upgrade

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/esp"
)

// TestPrevious finds the image a rollback boots: of the image's entries below the booted one's
// generation, the highest that is not bad and boots another store.
func TestPrevious(t *testing.T) {
	root := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	// entry is a UKI of the image as esp.Entries lists it: its ID is its name without the tries.
	entry := func(file string, left int, r []byte) esp.Entry {
		id := file
		if plus := strings.LastIndexByte(file, '+'); plus >= 0 {
			id = file[:plus] + ".efi"
		}
		return esp.Entry{File: file, ID: id, TriesLeft: left, TriesDone: -1, Readable: true, ImageID: "chalkos", RootHash: r}
	}
	booted := entry("chalkos_3.efi", -1, root(3))
	for _, tc := range []struct {
		name    string
		entries []esp.Entry
		want    string
	}{
		{"one below", []esp.Entry{entry("chalkos_2.efi", -1, root(2)), booted}, "chalkos_2.efi"},
		{"the highest below", []esp.Entry{entry("chalkos_1.efi", -1, root(1)), entry("chalkos_2.efi", -1, root(2)), booted}, "chalkos_2.efi"},
		{"bad", []esp.Entry{entry("chalkos_2+0-3.efi", 0, root(2)), booted}, ""},
		{"above", []esp.Entry{booted, entry("chalkos_4+3.efi", 3, root(4))}, ""},
		{"the running store", []esp.Entry{entry("chalkos_2.efi", -1, root(3)), booted}, ""},
		{"another image", []esp.Entry{{File: "rescue_2.efi", ID: "rescue_2.efi", TriesLeft: -1, Readable: true, ImageID: "rescue", RootHash: root(2)}, booted}, ""},
		{"none", []esp.Entry{booted}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Previous(tc.entries, "chalkos", booted, root(3))
			if ok != (tc.want != "") || got.File != tc.want {
				t.Errorf("previous = %q, %v; want %q", got.File, ok, tc.want)
			}
		})
	}
	if _, ok := Previous([]esp.Entry{entry("chalkos_2.efi", -1, root(2)), entry("chalkos_x.efi", -1, root(3))}, "chalkos", entry("chalkos_x.efi", -1, root(3)), root(3)); ok {
		t.Error("a booted entry whose name gives no generation has a previous image")
	}
}

// TestRollback boots the image before an upgrade again without writing its slot: its UKI is
// renamed to the next generation with its tries, boot counting falls back to the running image
// while it is not blessed, and once it is, a second rollback boots the upgrade's image again.
func TestRollback(t *testing.T) {
	a, b := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, a)
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	l.runFrom(b, "chalkos_2.efi", 4, 5)
	l.writes = map[int]int{}
	ctx := context.Background()

	recorded := 0
	record := func() error {
		recorded++
		return nil
	}
	res, err := l.node.Rollback(ctx, a.root, record)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_3+3.efi" || res.Wrote || len(l.writes) != 0 {
		t.Errorf("result %+v, writes %v; want a's UKI as generation 3 and nothing written", res, l.writes)
	}
	l.boots(a, 3)
	if again, err := l.node.Rollback(ctx, a.root, record); err != nil || !again.AlreadyStaged || again.Entry != "chalkos_3+3.efi" {
		t.Errorf("a second request = %+v, %v; want it staged already", again, err)
	}
	if same, err := l.node.Rollback(ctx, b.root, record); err != nil || !same.AlreadyInstalled {
		t.Errorf("a rollback to the running image = %+v, %v", same, err)
	}
	// The rollback and the request that found its image staged are recorded; one to the running
	// image is not.
	if recorded != 2 {
		t.Errorf("recorded %d times, want 2", recorded)
	}
	// Boots that are never found healthy use the tries up, and the disk boots b again.
	for range 3 {
		if e := l.attempt(); !bytes.Equal(e.RootHash, a.root) {
			t.Fatalf("booted %s", e.File)
		}
	}
	if e, name := l.boot(); name != "b" || e.File != "chalkos_2.efi" {
		t.Errorf("after the tries the disk boots %s from %s, want b", name, e.File)
	}
	if _, err := l.node.Rollback(ctx, a.root, nil); err == nil || !strings.Contains(err.Error(), "keeps no image it ran before") {
		t.Errorf("after the fallback: %v", err)
	}
}

// TestRollbackStopped stops a rollback before it renames the UKI, by a failed record or a failed
// rename: the disk keeps booting the running image, and the rollback asked for again does it.
func TestRollbackStopped(t *testing.T) {
	a, b := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, a)
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	l.runFrom(b, "chalkos_2.efi", 4, 5)
	stop := errors.New("stopped")
	var changes []string
	l.node.Change = func(what string) error {
		changes = append(changes, what)
		return stop
	}
	if _, err := l.node.Rollback(context.Background(), a.root, func() error { return stop }); !errors.Is(err, stop) || len(changes) != 0 {
		t.Fatalf("rollback = %v with changes %q, want it stopped by the record before any change", err, changes)
	}
	if _, err := l.node.Rollback(context.Background(), a.root, nil); !errors.Is(err, stop) {
		t.Fatalf("rollback = %v, want it stopped", err)
	}
	if len(changes) != 1 || changes[0] != "name the UKI chalkos_3+3.efi" {
		t.Errorf("changes = %q, want the rename alone", changes)
	}
	if e, name := l.boot(); name != "b" || e.File != "chalkos_2.efi" {
		t.Errorf("the disk boots %s from %s, want b", name, e.File)
	}
	l.node.Change = nil
	if res, err := l.node.Rollback(context.Background(), a.root, nil); err != nil || res.Entry != "chalkos_3+3.efi" {
		t.Fatalf("rollback again = %+v, %v", res, err)
	}
	l.boots(a, 3)
}

// TestRollbackTwice rolls back, blesses the boot and rolls back again, to the image the first
// rollback left.
func TestRollbackTwice(t *testing.T) {
	a, b := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, a)
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	l.runFrom(b, "chalkos_2.efi", 4, 5)
	if _, err := l.node.Rollback(context.Background(), a.root, nil); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	l.runFrom(a, "chalkos_3.efi", 2, 3)
	res, err := l.node.Rollback(context.Background(), b.root, nil)
	if err != nil || res.Entry != "chalkos_4+2.efi" {
		t.Fatalf("second rollback = %+v, %v", res, err)
	}
	l.boots(b, 2)
}

// TestRollbackRefuses refuses a node without an image it ran before, another root hash than that
// image's, a staged upgrade, which replaced it, a UKI its check refuses and an inactive slot that
// no longer holds the store; the disk keeps booting the running image.
func TestRollbackRefuses(t *testing.T) {
	a, b, c := newImage(t, "a", 3), newImage(t, "b", 2), newImage(t, "c", 1)
	ctx := context.Background()
	l := newLab(t, a)
	// A refused rollback records nothing.
	record := func() error {
		t.Error("recorded a refused rollback")
		return nil
	}
	if _, err := l.node.Rollback(ctx, b.root, record); err == nil || !strings.Contains(err.Error(), "keeps no image it ran before") {
		t.Errorf("without one: %v", err)
	}
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	if _, err := l.node.Rollback(ctx, c.root, record); err == nil || !strings.Contains(err.Error(), "is staged to boot next from chalkos_2+2.efi") {
		t.Errorf("a staged upgrade: %v", err)
	}
	l.bless(l.attempt())
	l.runFrom(b, "chalkos_2.efi", 4, 5)
	if _, err := l.node.Rollback(ctx, c.root, record); err == nil || !strings.Contains(err.Error(), "the image the node ran before is") {
		t.Errorf("another root hash: %v", err)
	}
	// The UKI is checked again, as an upgrade checks a new one.
	platform := l.node.OSRelease["CHALKOS_PLATFORM"]
	l.node.OSRelease["CHALKOS_PLATFORM"] = "elsewhere"
	if _, err := l.node.Rollback(ctx, a.root, record); err == nil || !strings.Contains(err.Error(), "the UKI's platform is") {
		t.Errorf("a UKI of another platform: %v", err)
	}
	l.node.OSRelease["CHALKOS_PLATFORM"] = platform
	l.setUUID(2, "11111111-2222-3333-4444-555555555555")
	if _, err := l.node.Rollback(ctx, a.root, record); err == nil || !strings.Contains(err.Error(), "the inactive slot no longer holds the store") {
		t.Errorf("a changed slot: %v", err)
	}
	if e, name := l.boot(); name != "b" || e.File != "chalkos_2.efi" {
		t.Errorf("the disk boots %s from %s, want b", name, e.File)
	}
}

// TestRollbackBeforeTheBootIsHealthy rolls back while the upgrade's boot has not been found
// healthy: the image before gets the highest generation without tries, so the node keeps a
// known-good entry, and asking again finds it staged. From that image, whose entry is uncounted,
// a rollback to the upgrade's image is counted again.
func TestRollbackBeforeTheBootIsHealthy(t *testing.T) {
	a, b := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, a)
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	e := l.attempt()
	l.runFrom(b, e.File, 4, 5)
	ctx := context.Background()

	res, err := l.node.Rollback(ctx, a.root, nil)
	if err != nil || res.Entry != "chalkos_3.efi" || res.AlreadyStaged {
		t.Fatalf("rollback = %+v, %v; want a's UKI as generation 3 without tries", res, err)
	}
	entries, err := esp.Entries(l.esp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if g := esp.EntryGeneration(e.ID, "chalkos"); g >= 3 && e.File != "chalkos_3.efi" {
			t.Errorf("%s is above a's entry", e.File)
		}
	}
	if e, name := l.boot(); name != "a" || e.File != "chalkos_3.efi" || e.TriesLeft != -1 {
		t.Errorf("the disk boots %s from %s (%d tries left), want a from chalkos_3.efi uncounted", name, e.File, e.TriesLeft)
	}
	if again, err := l.node.Rollback(ctx, a.root, nil); err != nil || !again.AlreadyStaged || again.Entry != "chalkos_3.efi" {
		t.Errorf("a second request = %+v, %v; want it staged already", again, err)
	}
	if _, err := l.node.Rollback(ctx, newImage(t, "c", 1).root, nil); err == nil || !strings.Contains(err.Error(), "is staged to boot next from chalkos_3.efi") {
		t.Errorf("a rollback to another image: %v", err)
	}
	// An upgrade to the image finds it staged. A refusal of its check keeps it: the node falls
	// back to it.
	if res, err := l.install(a); err != nil || !res.AlreadyStaged || res.Entry != "chalkos_3.efi" {
		t.Errorf("an upgrade to a = %+v, %v; want it staged already", res, err)
	}
	var calls []string
	run := l.node.Run
	l.node.Run = storeRunner{Runner: run, calls: &calls, mounted: func(dir string) { writeStore(t, dir) }}
	l.node.StoreMount = filepath.Join(t.TempDir(), "store")
	l.node.CheckStore = func(Store) error { return errors.New("the config does not suit the image") }
	var retired *StagedRetiredError
	if _, err := l.install(a); err == nil || !strings.Contains(err.Error(), "does not suit") || errors.As(err, &retired) {
		t.Errorf("an upgrade to a refused by its check = %v, want the refusal alone", err)
	}
	l.node.Run, l.node.CheckStore = run, nil
	if e, name := l.boot(); name != "a" || e.File != "chalkos_3.efi" {
		t.Errorf("after a refused upgrade the disk boots %s from %s, want a from chalkos_3.efi", name, e.File)
	}
	if e := l.attempt(); e.File != "chalkos_3.efi" {
		t.Fatalf("booted %s", e.File)
	}
	l.runFrom(a, "chalkos_3.efi", 2, 3)
	res, err = l.node.Rollback(ctx, b.root, nil)
	if err != nil || res.Entry != "chalkos_4+2.efi" {
		t.Fatalf("rollback to b = %+v, %v; want it counted", res, err)
	}
	l.boots(b, 2)
}
