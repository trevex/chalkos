package upgrade

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

// TestUpgradeAndFallBack installs an image as the next generation, boots it until its tries are
// used up, and finds the disk booting the image before; installed again, the image gets another
// generation, and once blessed, a boot of it is final.
func TestUpgradeAndFallBack(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	l := newLab(t, old)
	oldUKI := l.file("chalkos_1.efi")
	rescue := newUKI(map[string]string{"IMAGE_ID": "rescue", "IMAGE_VERSION": "1"}, old.root)
	os.WriteFile(filepath.Join(l.esp, linuxDir, "rescue_7.efi"), rescue, 0o644)
	// A UKI is of the image its os-release names, whatever its file is called.
	other := newUKI(map[string]string{"IMAGE_ID": "rescue", "IMAGE_VERSION": "2"}, newImage(t, "r", 3).root)
	os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_rescue.efi"), other, 0o644)
	res, err := l.install(img)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_2+2.efi" || !res.Wrote {
		t.Errorf("result %+v", res)
	}
	parts := l.table()
	if parts[4].Name != "store-verity_"+LabelID(img.root) || parts[5].Name != "store_"+LabelID(img.root) ||
		parts[2].Name != "store-verity_"+LabelID(old.root) || parts[3].Name != "store_"+LabelID(old.root) {
		t.Errorf("labels %q %q %q %q", parts[2].Name, parts[3].Name, parts[4].Name, parts[5].Name)
	}
	if !bytes.Equal(l.file("chalkos_1.efi"), oldUKI) {
		t.Error("the running UKI changed")
	}
	l.boots(img, 2)
	if !bytes.Equal(l.file("rescue_7.efi"), rescue) || !bytes.Equal(l.file("chalkos_rescue.efi"), other) {
		t.Error("a UKI of another image changed")
	}

	// Two boots that are never found healthy use the tries up.
	for range 2 {
		if e := l.attempt(); !bytes.Equal(e.RootHash, img.root) {
			t.Fatalf("booted %s", e.File)
		}
	}
	if e, name := l.boot(); name != "a" || e.File != "chalkos_1.efi" {
		t.Errorf("after the tries the disk boots %s from %s, want a", name, e.File)
	}
	if l.file("chalkos_2+0-2.efi") == nil {
		t.Error("the failed UKI is gone")
	}

	// Installed again, the image is the next generation, and the entry that failed is retired.
	if res, err = l.install(img); err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_3+2.efi" || l.file("chalkos_2+0-2.efi") != nil {
		t.Errorf("installed again as %s, the failed UKI kept: %v", res.Entry, l.file("chalkos_2+0-2.efi") != nil)
	}
	l.boots(img, 2)
	l.bless(l.attempt())
	for range 3 {
		if e := l.attempt(); e.File != "chalkos_3.efi" {
			t.Errorf("a blessed boot booted %s", e.File)
		}
	}
}

// TestVerityBlockSizes installs images whose stores have dm-verity blocks of 4 KiB, as images
// are built, or of 512 bytes, as they were, over each other, and boots them.
func TestVerityBlockSizes(t *testing.T) {
	// 200 blocks of 4 KiB make a tree of two levels and fit the lab's slots.
	const blocks4K = 200
	for _, tc := range []struct {
		name         string
		running, img image
	}{
		{"512 to 4096", newImage(t, "a", 3), newImageOfBlocks(t, "b", 2, 4096, blocks4K)},
		{"4096 to 4096", newImageOfBlocks(t, "a", 3, 4096, blocks4K), newImageOfBlocks(t, "b", 2, 4096, blocks4K)},
		{"4096 to 512", newImageOfBlocks(t, "a", 3, 4096, blocks4K), newImage(t, "b", 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, tc.running)
			if _, err := l.install(tc.img); err != nil {
				t.Fatal(err)
			}
			l.boots(tc.img, 2)
			l.bless(l.attempt())
			if e, name := l.boot(); name != tc.img.name || e.File != "chalkos_2.efi" {
				t.Errorf("a blessed boot booted %q from %s", name, e.File)
			}
		})
	}
}

// TestFourKnDisk upgrades a node whose disk has 4096-byte sectors, which sfdisk counts sizes in: a
// store larger than an eighth of the 1 MiB slot fits, and one larger than the slot is refused
// before anything changes.
func TestFourKnDisk(t *testing.T) {
	old := newImageOfBlocks(t, "a", 3, 4096, 100)
	l := newLabOf(t, old, 4096)
	img := newImageOfBlocks(t, "b", 2, 4096, 200)
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.boots(img, 2)

	l = newLabOf(t, old, 4096)
	before := l.table()
	l.node.Change = func(what string) error {
		t.Errorf("changed: %s", what)
		return errCrash
	}
	if _, err := l.install(newImageOfBlocks(t, "b", 2, 4096, 300)); err == nil || !strings.Contains(err.Error(), "do not fit the slot's partitions of 1048576 and 262144 bytes") {
		t.Errorf("install = %v, want the store refused as too large", err)
	}
	for n, p := range l.table() {
		if before[n] != p {
			t.Errorf("partition %d changed", n)
		}
	}
}

// TestDowngrade goes back to the image the node ran before: it becomes the next generation, so it
// boots first, from the slot that still holds its store, and the disk falls back to the newer
// image once its tries are used up.
func TestDowngrade(t *testing.T) {
	older, newer := newImage(t, "a", 1), newImage(t, "b", 3)
	l := newLab(t, older)
	if _, err := l.install(newer); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	l.runFrom(newer, "chalkos_2.efi", 4, 5)
	l.writes = map[int]int{}
	res, err := l.install(older)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_3+1.efi" || res.Wrote || len(l.writes) != 0 {
		t.Errorf("result %+v, writes %v; want the older image's slot reused as generation 3", res, l.writes)
	}
	l.boots(older, 1)
	l.attempt()
	if e, name := l.boot(); name != "b" || e.File != "chalkos_2.efi" {
		t.Errorf("after the tries the disk boots %s from %s, want b", name, e.File)
	}
	if _, err := l.install(older); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	if e := l.attempt(); e.File != "chalkos_4.efi" {
		t.Errorf("after blessing the downgrade the disk boots %s", e.File)
	}
}

// TestGenerationsPastNine upgrades a node again and again: each upgrade's entry is the next
// generation, and chalkos_10.efi boots before chalkos_9.efi, as systemd-boot compares numbers in
// IDs as numbers.
func TestGenerationsPastNine(t *testing.T) {
	images := []image{newImage(t, "a", 3), newImage(t, "b", 3)}
	l := newLab(t, images[0])
	for generation := 2; generation <= 11; generation++ {
		img := images[(generation-1)%2]
		res, err := l.install(img)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("chalkos_%d+3.efi", generation); res.Entry != want {
			t.Fatalf("entry %s, want %s", res.Entry, want)
		}
		l.boots(img, 3)
		e := l.attempt()
		l.bless(e)
		// The images take turns in the slots: slot B, partitions 4 and 5, holds the even generations.
		verity := 2
		if generation%2 == 0 {
			verity = 4
		}
		l.runFrom(img, e.ID, verity, verity+1)
	}
	if e, name := l.boot(); name != "a" || e.File != "chalkos_11.efi" {
		t.Errorf("the disk boots %s from %s, want a from chalkos_11.efi", name, e.File)
	}
}

// TestGenerationsByName counts the generation a name on the ESP gives the image, whatever its UKI
// holds, as systemd-boot orders the entry by its name: an upgrade's entry is the next one. A UKI
// that cannot be read is retired, as the image's other entries; one of another image stays, as
// does one whose os-release names no image ID, which systemd-boot sorts by its ID.
func TestGenerationsByName(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	rescue := newUKI(map[string]string{"IMAGE_ID": "rescue", "IMAGE_VERSION": "1"}, old.root)
	nixos := newUKI(map[string]string{"ID": "nixos", "VERSION_ID": "26.05"}, newImage(t, "c", 3).root)
	for _, tc := range []struct {
		name, file string
		uki        []byte
		want       string
		kept       bool
	}{
		{"an unreadable UKI", "chalkos_9.efi", []byte("not a UKI"), "chalkos_10+3.efi", false},
		{"another image's", "chalkos_2.efi", rescue, "chalkos_3+3.efi", true},
		{"one without an image ID", "chalkos_5.efi", nixos, "chalkos_6+3.efi", true},
		{"a huge generation", "chalkos_18446744073709551614.efi", []byte("not a UKI"), "chalkos_18446744073709551615+3.efi", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			if err := os.WriteFile(filepath.Join(l.esp, linuxDir, tc.file), tc.uki, 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := l.install(img)
			if err != nil || res.Entry != tc.want {
				t.Fatalf("install = %+v, %v; want %s", res, err, tc.want)
			}
			if kept := l.file(tc.file) != nil; kept != tc.kept {
				t.Errorf("%s kept: %v, want %v", tc.file, kept, tc.kept)
			}
			l.boots(img, 3)
		})
	}
}

// TestUpgradeFromSlotB runs the image of slot B and installs the next into slot A, never
// writing slot B or its UKI.
func TestUpgradeFromSlotB(t *testing.T) {
	a, b, c := newImage(t, "a", 3), newImage(t, "b", 3), newImage(t, "c", 3)
	l := newLab(t, a)
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	e := l.attempt()
	l.bless(e)
	l.runFrom(b, "chalkos_2.efi", 4, 5)
	l.writes = map[int]int{}
	bUKI := l.file("chalkos_2.efi")
	res, err := l.install(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_3+3.efi" {
		t.Errorf("entry %s", res.Entry)
	}
	if l.writes[2] != 1 || l.writes[3] != 1 || l.writes[4]+l.writes[5] != 0 {
		t.Errorf("writes %v, want partitions 2 and 3 once", l.writes)
	}
	if l.file("chalkos_1.efi") != nil {
		t.Error("slot A's old UKI stayed")
	}
	if !bytes.Equal(l.file("chalkos_2.efi"), bUKI) {
		t.Error("the running UKI changed")
	}
	l.boots(c, 3)
}

// TestAlreadyStaged installs the image that boots next again: nothing changes while its entry has
// tries left and the slot holds its store. Once the slot was changed, or the tries were used up,
// it is installed anew, as the next generation.
func TestAlreadyStaged(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	unchanged := func(want string) {
		t.Helper()
		l.node.Change = func(what string) error {
			t.Errorf("changed: %s", what)
			return errCrash
		}
		defer func() { l.node.Change = nil }()
		res, err := l.install(img)
		if err != nil || !res.AlreadyStaged || res.Entry != want || res.Wrote {
			t.Errorf("installing the staged image: %+v, %v; want it staged as %s", res, err, want)
		}
	}
	unchanged("chalkos_2+3.efi")
	// A boot of it that was not found healthy, after which the node runs the old image again.
	l.attempt()
	unchanged("chalkos_2+2-1.efi")

	// Another program changed the slot's UUIDs, so the entry would find no store.
	l.setUUID(5, randomUUID())
	res, err := l.install(img)
	if err != nil || res.AlreadyStaged || res.Entry != "chalkos_3+3.efi" {
		t.Errorf("installing the image into a changed slot: %+v, %v", res, err)
	}
	l.boots(img, 3)

	// Its tries used up, the image is installed again.
	for range 3 {
		l.attempt()
	}
	if res, err = l.install(img); err != nil || res.AlreadyStaged || res.Entry != "chalkos_4+3.efi" {
		t.Errorf("installing the image whose tries were used up: %+v, %v", res, err)
	}
	l.boots(img, 3)
}

var errCrash = errors.New("crash")

// TestInterruptedUpgrade stops an upgrade before each change it makes: the disk boots the running
// image or the new one with all its tries, and the upgrade run again completes, without writing
// the slot again once it held the image.
func TestInterruptedUpgrade(t *testing.T) {
	a, b, c := newImage(t, "a", 3), newImage(t, "b", 3), newImage(t, "c", 3)
	for _, start := range []struct {
		name string
		lab  func(t *testing.T) *lab
		img  image
	}{
		{"from slot A with slot B empty", func(t *testing.T) *lab { return newLab(t, a) }, b},
		// The retirement removes slot A's UKI and labels.
		{"from slot B with slot A holding an image", func(t *testing.T) *lab {
			l := newLab(t, a)
			if _, err := l.install(b); err != nil {
				t.Fatal(err)
			}
			l.bless(l.attempt())
			l.runFrom(b, "chalkos_2.efi", 4, 5)
			return l
		}, c},
		// The retirement removes the UKI of an image the node fell back from and a UKI left half
		// written.
		{"with a failed image and a temporary file", func(t *testing.T) *lab {
			l := newLab(t, a)
			if _, err := l.install(b); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				l.attempt()
			}
			if err := os.WriteFile(filepath.Join(l.esp, linuxDir, tempPrefix+hex.EncodeToString(b.root)+".efi"), b.uki[:100], 0o644); err != nil {
				t.Fatal(err)
			}
			return l
		}, c},
	} {
		t.Run(start.name, func(t *testing.T) {
			var changes []string
			l := start.lab(t)
			l.node.Change = func(what string) error {
				changes = append(changes, what)
				return nil
			}
			full, err := l.install(start.img)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("changes: %q", changes)
			// A slot that held the image before is never written; one that did not is written until
			// the hash tree was.
			written := !full.Wrote
			for i, change := range changes {
				written = written || i > 0 && changes[i-1] == "write the hash tree"
				t.Run(change, func(t *testing.T) {
					l := start.lab(t)
					calls := 0
					l.node.Change = func(string) error {
						calls++
						if calls == i+1 {
							return errCrash
						}
						return nil
					}
					if _, err := l.install(start.img); !errors.Is(err, errCrash) {
						t.Fatalf("install = %v, want the crash", err)
					}
					l.bootsRunningOr(start.img, 3)
					l.node.Change = nil
					res, err := l.install(start.img)
					if err != nil {
						t.Fatal(err)
					}
					if res.Wrote == written {
						t.Errorf("wrote the slot: %v, after the crash at %q", res.Wrote, change)
					}
					l.boots(start.img, 3)
				})
			}
		})
	}
}

// TestInterruptedTransfer cuts the stream at points within the store, the hash tree and the UKI.
func TestInterruptedTransfer(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	store, hash, uki := int64(len(img.store)), int64(len(img.hash)), int64(len(img.uki))
	for _, cut := range []int64{0, store / 2, store, store + hash/2, store + hash, store + hash + uki/2, store + hash + uki - 1} {
		l := newLab(t, old)
		if _, err := l.node.Install(context.Background(), img.header, io.LimitReader(img.stream(), cut)); err == nil || !strings.Contains(err.Error(), "receive") {
			t.Errorf("a stream cut after %d bytes: %v", cut, err)
		}
		l.bootsRunningOr(img, 3)
		if _, err := l.install(img); err != nil {
			t.Fatal(err)
		}
		l.boots(img, 3)
	}
}

// TestRefusesBeforeChanging checks what is refused before anything changes.
func TestRefusesBeforeChanging(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	for _, tc := range []struct {
		name   string
		header func(h *Header)
		setup  func(l *lab)
		want   string
	}{
		{"another image ID", func(h *Header) { h.ImageID = "other" }, nil, "image ID is other"},
		{"another platform", func(h *Header) { h.Platform = "kvm" }, nil, "platform is kvm, the node's metal"},
		{"no platform", func(h *Header) { h.Platform = "" }, nil, "platform are required"},
		{"another architecture", func(h *Header) { h.Architecture = "arm64" }, nil, "the image is built for arm64, this machine for x86-64"},
		{"a store larger than the slot", func(h *Header) { h.StoreSize = 2 << 20 }, nil, "do not fit"},
		// The node booted generation 2, which is counted, and falls back to generation 1.
		{"a running boot not found healthy", nil, func(l *lab) {
			os.Rename(filepath.Join(l.esp, linuxDir, "chalkos_1.efi"), filepath.Join(l.esp, linuxDir, "chalkos_2+2-1.efi"))
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_1.efi"), newImage(t, "z", 3).uki, 0o644)
			l.setVariable("LoaderEntrySelected", "chalkos_2.efi")
		}, "not been found healthy"},
		{"an entry that boots another store", nil, func(l *lab) { l.setVariable("LoaderEntrySelected", "chalkos_9.efi") }, "not on the ESP"},
		{"no second slot", nil, func(l *lab) {
			if out, err := l.sfdisk("--delete", l.disk, "5").CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		}, "needs two slots"},
		{"both slots carrying the running store's UUIDs", nil, func(l *lab) {
			data, verity := PartitionUUIDs(old.root)
			l.setUUID(4, verity)
			l.setUUID(5, data)
		}, "both slots"},
		{"the inactive slot carrying a UUID of the running store", nil, func(l *lab) {
			data, _ := PartitionUUIDs(old.root)
			l.setUUID(5, data)
		}, "the inactive slot carries"},
		// Names chalkd never writes, placed by hand or corrupted.
		{"a booted entry with a leading zero", nil, func(l *lab) {
			os.Rename(filepath.Join(l.esp, linuxDir, "chalkos_1.efi"), filepath.Join(l.esp, linuxDir, "chalkos_01.efi"))
			l.setVariable("LoaderEntrySelected", "chalkos_01.efi")
		}, "chalkos_01.efi on the ESP is not named chalkos_<generation>.efi"},
		// A copy of the running UKI the upgrade keeps, which systemd-boot may sort above the new
		// entry.
		{"a kept copy with a leading zero", nil, func(l *lab) {
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_01.efi"), old.uki, 0o644)
		}, "chalkos_01.efi on the ESP is not named chalkos_<generation>.efi"},
		{"a kept copy with a counter past an int32", nil, func(l *lab) {
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_7+2147483648.efi"), old.uki, 0o644)
		}, "chalkos_7+2147483648.efi on the ESP is not named chalkos_<generation>.efi"},
		{"a kept copy named by no generation", nil, func(l *lab) {
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_5.1.efi"), old.uki, 0o644)
		}, "chalkos_5.1.efi on the ESP is not named chalkos_<generation>.efi"},
		{"a generation past a uint64's range", nil, func(l *lab) {
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_18446744073709551616.efi"), []byte("not a UKI"), 0o644)
		}, "too large"},
		{"no generation after the highest", nil, func(l *lab) {
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_18446744073709551615.efi"), []byte("not a UKI"), 0o644)
		}, "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			if tc.setup != nil {
				tc.setup(l)
			}
			before := l.table()
			esp, _ := os.ReadDir(filepath.Join(l.esp, linuxDir))
			l.node.Change = func(what string) error {
				t.Errorf("changed: %s", what)
				return errCrash
			}
			h := img.header
			if tc.header != nil {
				tc.header(&h)
			}
			_, err := l.node.Install(context.Background(), h, img.stream())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("install = %v, want %q", err, tc.want)
			}
			for n, p := range l.table() {
				if before[n] != p {
					t.Errorf("partition %d changed", n)
				}
			}
			if after, _ := os.ReadDir(filepath.Join(l.esp, linuxDir)); len(after) != len(esp) {
				t.Errorf("the ESP holds %d files, %d before", len(after), len(esp))
			}
		})
	}

	l := newLab(t, old)
	l.node.Change = func(what string) error {
		t.Errorf("changed: %s", what)
		return errCrash
	}
	if res, err := l.install(old); err != nil || !res.AlreadyInstalled {
		t.Errorf("installing the running image: %+v, %v", res, err)
	}
}

// TestRefusesBeforeActivating checks what is refused once the image arrived: the slot stays
// retired and the disk boots the running image.
func TestRefusesBeforeActivating(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	flip := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 1
		return c
	}
	withOSRelease := func(edit func(map[string]string)) image {
		c := img
		osRelease := img.osRelease(3)
		edit(osRelease)
		c.uki = newUKI(osRelease, img.root)
		return withHeader(c)
	}
	otherStore := img
	otherStore.uki = newUKI(img.osRelease(3), old.root)
	noTries := img
	noTries.uki = newUKI(img.osRelease(0), img.root)
	otherArch := img
	otherArch.uki = ukitest.WithMachine(img.uki, 0xaa64)
	// Another store than the running one: an upgrade carrying that is refused before it starts.
	wrongRoot := img
	wrongRoot.root = newImage(t, "c", 3).root
	for _, tc := range []struct {
		name string
		img  image
		want string
	}{
		{"a corrupted store", func() image { c := img; c.store = flip(img.store, 100); return c }(), "store's SHA-256"},
		{"a corrupted hash tree", func() image { c := img; c.hash = flip(img.hash, 600); return c }(), "hash tree's SHA-256"},
		{"a store that does not match the root hash", withHeader(wrongRoot), "root hash"},
		{"a corrupted UKI", func() image { c := img; c.uki = flip(img.uki, 700); return c }(), "UKI's SHA-256"},
		{"a UKI of another platform", withOSRelease(func(o map[string]string) { o["CHALKOS_PLATFORM"] = "kvm" }), "platform is \"kvm\""},
		// systemd-boot would order the entry by the version before its generation.
		{"a UKI with an image version", withOSRelease(func(o map[string]string) { o["IMAGE_VERSION"] = "0.3.0" }), "sets IMAGE_VERSION to \"0.3.0\""},
		{"a UKI without IMAGE_VERSION", withOSRelease(func(o map[string]string) { delete(o, "IMAGE_VERSION") }), "has no IMAGE_VERSION"},
		{"a UKI booting another store", withHeader(otherStore), "boots another store"},
		{"a UKI without boot tries", withHeader(noTries), "boot tries"},
		{"a UKI of another architecture", withHeader(otherArch), "the UKI is built for arm64, but the image names x86-64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			_, err := l.node.Install(context.Background(), tc.img.header, io.MultiReader(bytes.NewReader(tc.img.store), bytes.NewReader(tc.img.hash), bytes.NewReader(tc.img.uki)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want %q", err, tc.want)
			}
			if e, name := l.boot(); name != "a" {
				t.Errorf("the disk boots %s from %s", name, e.File)
			}
			if p := l.table(); p[4].Name != emptyLabel || p[5].Name != emptyLabel {
				t.Errorf("the slot is labelled %q and %q", p[4].Name, p[5].Name)
			}
		})
	}
}

// withHeader gives an image the header of its store and UKI as they are.
func withHeader(img image) image {
	img.header = img.headerFor(img.uki)
	img.header.RootHash = img.root
	return img
}

// TestSecureBoot checks the UKI against the firmware's db when Secure Boot is on.
func TestSecureBoot(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	dir := t.TempDir()
	key, cert, certDER := testSigner(t, dir, "db")
	_, _, otherDER := testSigner(t, dir, "other")
	signed := img
	signed.uki = sbsign(t, dir, img.uki, key, cert)
	signed.header = signed.headerFor(signed.uki)

	for _, tc := range []struct {
		name       string
		secureBoot byte
		db         []byte
		img        image
		want       string
	}{
		{"unsigned", 1, certDER, img, "Secure Boot would refuse the UKI: the image is not signed"},
		{"signed by another key", 1, otherDER, signed, "not in db"},
		{"signed", 1, certDER, signed, ""},
		{"Secure Boot off", 0, otherDER, img, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			l.setGlobal("SecureBoot", globalVendor, []byte{tc.secureBoot})
			l.setGlobal("SetupMode", globalVendor, []byte{0})
			l.setGlobal("db", securityVendor, ukitest.SignatureList(tc.db))
			l.setGlobal("dbx", securityVendor, nil)
			_, err := l.install(tc.img)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				l.boots(img, 3)
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want %q", err, tc.want)
			}
			if _, name := l.boot(); name != "a" {
				t.Errorf("the disk boots %s", name)
			}
		})
	}
}

func testSigner(t *testing.T, dir, name string) (key, cert string, der []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	key, cert = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	return key, cert, der
}

func sbsign(t *testing.T, dir string, image []byte, key, cert string) []byte {
	t.Helper()
	in, out := filepath.Join(dir, "in.efi"), filepath.Join(dir, "out.efi")
	if err := os.WriteFile(in, image, 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("sbsign", "--key", key, "--cert", cert, "--output", out, in).CombinedOutput(); err != nil {
		t.Fatalf("sbsign: %v: %s", err, o)
	}
	signed, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// TestRebootHelps decides whether a reboot after an unhealthy boot leads the boot loader on.
func TestRebootHelps(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 2)
	for _, tc := range []struct {
		name   string
		booted string
		others map[string]image
		want   bool
	}{
		{"a blessed boot", "chalkos_2.efi", map[string]image{"chalkos_1.efi": old}, false},
		{"a try with tries left", "chalkos_2+1-1.efi", map[string]image{"chalkos_1.efi": old}, true},
		{"the last try", "chalkos_2+0-2.efi", map[string]image{"chalkos_1.efi": old}, true},
		{"the last try with a bad image besides", "chalkos_2+0-2.efi", map[string]image{"chalkos_1+0-3.efi": old}, false},
		{"the last try alone", "chalkos_2+0-2.efi", nil, false},
		{"a boot past its tries", "chalkos_2+0-5.efi", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			os.Remove(filepath.Join(l.esp, linuxDir, "chalkos_1.efi"))
			os.WriteFile(filepath.Join(l.esp, linuxDir, tc.booted), img.uki, 0o644)
			for name, other := range tc.others {
				os.WriteFile(filepath.Join(l.esp, linuxDir, name), other.uki, 0o644)
			}
			l.setVariable("LoaderEntrySelected", "chalkos_2.efi")
			entries, err := Entries(l.esp)
			if err != nil {
				t.Fatal(err)
			}
			got, why := RebootHelps(entries, l.efivars, "usrhash="+hex.EncodeToString(img.root))
			if got != tc.want {
				t.Errorf("RebootHelps = %v (%s), want %v", got, why, tc.want)
			}
		})
	}
}

// tableLock is a lock that knows whether it is held.
type tableLock struct {
	mu   sync.Mutex
	held atomic.Bool
}

func (l *tableLock) Lock()   { l.mu.Lock(); l.held.Store(true) }
func (l *tableLock) Unlock() { l.held.Store(false); l.mu.Unlock() }

// watchedRunner runs commands as the runner it wraps, after showing each to check.
type watchedRunner struct {
	node.Runner
	check func(ctx context.Context, name string, args []string)
}

func (r watchedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.check(ctx, name, args)
	return r.Runner.Run(ctx, name, args...)
}

// TestPartitionTableLocks checks that every read and change of the partition table runs while
// the node holds the lock it shares with chalkd's storage changes, and takes the disk's lock
// against other tools.
func TestPartitionTableLocks(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	lock := &tableLock{}
	l.node.TableLock = lock
	calls := 0
	l.node.Run = watchedRunner{Runner: l.node.Run, check: func(_ context.Context, name string, args []string) {
		if name != "sfdisk" {
			return
		}
		calls++
		if !lock.held.Load() {
			t.Errorf("sfdisk %v ran without the partition table's lock", args)
		}
		if !slices.Contains(args, "--lock=nonblock") {
			t.Errorf("sfdisk %v ran without locking the disk, or waits for its lock unbounded", args)
		}
	}}
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("no sfdisk call")
	}
	if lock.held.Load() {
		t.Error("the upgrade kept the partition table's lock")
	}
}

// TestChangesOutliveTheCaller cancels the upgrade's context as it changes the partition table: a
// client that goes away must not kill sfdisk while it writes.
func TestChangesOutliveTheCaller(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.node.Change = func(what string) error {
		if strings.HasPrefix(what, "set the UUID") || strings.HasPrefix(what, "label") {
			cancel()
		}
		return nil
	}
	l.node.Run = watchedRunner{Runner: l.node.Run, check: func(ctx context.Context, name string, args []string) {
		if name == "sfdisk" && slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--part-") }) && ctx.Err() != nil {
			t.Errorf("sfdisk %v ran with a cancelled context", args)
		}
	}}
	l.node.Install(ctx, img.header, img.stream())
}

// TestEntriesWhileBlessed lists the UKIs again when one is renamed while they are read, as
// systemd-bless-boot renames the booted one once the boot was found healthy.
func TestEntriesWhileBlessed(t *testing.T) {
	img := newImage(t, "b", 3)
	esp := t.TempDir()
	dir := filepath.Join(esp, linuxDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chalkos_2+2-1.efi"), img.uki, 0o644); err != nil {
		t.Fatal(err)
	}
	blessed := false
	list, err := entries(esp, func(path string) (*os.File, error) {
		if !blessed {
			blessed = true
			if err := os.Rename(path, filepath.Join(dir, "chalkos_2.efi")); err != nil {
				t.Fatal(err)
			}
		}
		return os.Open(path)
	})
	if err != nil || len(list) != 1 || list[0].File != "chalkos_2.efi" || list[0].Generation != 2 || list[0].TriesLeft != -1 {
		t.Errorf("entries = %+v, %v", list, err)
	}
}

// TestEntriesReadErrors lists a file that reads but is no UKI as one that cannot be read, and
// fails on a UKI that does not open or read: which store it boots is then unknown.
func TestEntriesReadErrors(t *testing.T) {
	img := newImage(t, "b", 3)
	esp := t.TempDir()
	dir := filepath.Join(esp, linuxDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"chalkos_1.efi": img.uki, "chalkos_2.efi": []byte("not a UKI")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, err := entries(esp, os.Open)
	if err != nil || len(list) != 2 || !list[0].Readable || !bytes.Equal(list[0].RootHash, img.root) || list[1].Readable || list[1].RootHash != nil {
		t.Fatalf("entries = %+v, %v", list, err)
	}
	for _, tc := range []struct {
		name string
		open func(string) (*os.File, error)
		want error
	}{
		{"open", func(string) (*os.File, error) { return nil, syscall.EACCES }, syscall.EACCES},
		// Reading a directory fails as a read of a broken file system does.
		{"read", func(string) (*os.File, error) { return os.Open(dir) }, syscall.EISDIR},
	} {
		if list, err := entries(esp, tc.open); !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "chalkos_1.efi on the ESP") {
			t.Errorf("entries failing to %s = %+v, %v; want %v", tc.name, list, err, tc.want)
		}
	}
}

// holdDisk takes the disk's BSD lock, as another program changing its partitions would, until
// the returned release.
func (l *lab) holdDisk() func() {
	l.t.Helper()
	f, err := os.Open(l.disk)
	if err != nil {
		l.t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		l.t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { f.Close() }) }
	l.t.Cleanup(release)
	return release
}

// TestDiskLockedElsewhere waits a bounded time for the disk's lock that another program holds,
// and fails the upgrade with why, instead of holding the partition table's lock, which chalkd's
// other requests wait for, as long as the other program holds the disk's.
func TestDiskLockedElsewhere(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	lock := &tableLock{}
	l.node.TableLock = lock
	l.node.LockWait = 500 * time.Millisecond
	release := l.holdDisk()
	// A program that holds the lock for good, which the test ends after a while.
	go func() {
		time.Sleep(5 * time.Second)
		release()
	}()
	start := time.Now()
	_, err := l.install(img)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("install = %v, want the disk locked", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the upgrade waited %v for the disk's lock", d)
	}
	if lock.held.Load() {
		t.Error("the upgrade kept the partition table's lock")
	}
	if e, name := l.boot(); name != "a" {
		t.Errorf("the disk boots %s from %s", name, e.File)
	}

	// A lock released within the wait is taken.
	l = newLab(t, old)
	l.node.LockWait = 10 * time.Second
	release = l.holdDisk()
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()
	if _, err := l.install(img); err != nil {
		t.Errorf("install once the disk's lock was released = %v", err)
	}
}

// tempUKIs are the UKIs on the ESP written under the temporary name.
func (l *lab) tempUKIs() []string {
	l.t.Helper()
	files, err := os.ReadDir(filepath.Join(l.esp, linuxDir))
	if err != nil {
		l.t.Fatal(err)
	}
	var temp []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), tempPrefix) {
			temp = append(temp, f.Name())
		}
	}
	return temp
}

// TestRemovesTheTemporaryUKI removes the UKI written under its temporary name when the upgrade
// fails after it was written, so it does not fill the ESP.
func TestRemovesTheTemporaryUKI(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	var changes []string
	l := newLab(t, old)
	l.node.Change = func(what string) error {
		changes = append(changes, what)
		return nil
	}
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	written := slices.Index(changes, "write the UKI")
	if written < 0 {
		t.Fatalf("no change writes the UKI: %q", changes)
	}
	for _, failing := range changes[written+1:] {
		t.Run(failing, func(t *testing.T) {
			l := newLab(t, old)
			l.node.Change = func(what string) error {
				if what == failing {
					return errCrash
				}
				return nil
			}
			if _, err := l.install(img); !errors.Is(err, errCrash) {
				t.Fatalf("install = %v, want the failure", err)
			}
			if temp := l.tempUKIs(); len(temp) != 0 {
				t.Errorf("the ESP keeps %q", temp)
			}
		})
	}
	// A UKI refused once written.
	otherPlatform := img
	osRelease := img.osRelease(3)
	osRelease["CHALKOS_PLATFORM"] = "kvm"
	otherPlatform.uki = newUKI(osRelease, img.root)
	l = newLab(t, old)
	if _, err := l.install(withHeader(otherPlatform)); err == nil {
		t.Fatal("installed a UKI of another platform")
	}
	if temp := l.tempUKIs(); len(temp) != 0 {
		t.Errorf("the ESP keeps %q", temp)
	}
}

// TestRefusesABootLoader refuses an image that brings a boot loader before anything changes: an
// upgrade leaves the boot loader as it is.
func TestRefusesABootLoader(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	l.node.Change = func(what string) error {
		t.Errorf("changed: %s", what)
		return nil
	}
	img.header.BootLoaderSize, img.header.BootLoaderSHA256 = 1, sum([]byte{1})
	stream := io.MultiReader(img.stream(), bytes.NewReader([]byte{1}))
	if _, err := l.node.Install(context.Background(), img.header, stream); err == nil || !strings.Contains(err.Error(), "boot loader") {
		t.Fatalf("install = %v, want the boot loader refused", err)
	}
}

// TestHeaderValidate checks the header's boot loader: optional, with its size and SHA-256
// together, and bounded.
func TestHeaderValidate(t *testing.T) {
	img := newImage(t, "b", 3)
	for _, tc := range []struct {
		name   string
		size   int64
		sha256 []byte
		want   string
	}{
		{"none", 0, nil, ""},
		{"one", 100, sum([]byte{1}), ""},
		{"no SHA-256", 100, nil, "together"},
		{"a SHA-256 alone", 0, sum([]byte{1}), "together"},
		{"too large", 64 << 20, sum([]byte{1}), "larger"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := img.header
			h.BootLoaderSize, h.BootLoaderSHA256 = tc.size, tc.sha256
			err := h.Validate()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestParseTable counts sizes in the table's sectors, which older sfdisk versions do not name:
// they are 512 bytes then.
func TestParseTable(t *testing.T) {
	for _, tc := range []struct {
		sector string
		want   int64
	}{{"", 2048 * 512}, {`"sectorsize": 512,`, 2048 * 512}, {`"sectorsize": 4096,`, 2048 * 4096}} {
		out := `{"partitiontable": {"label": "gpt", ` + tc.sector + ` "partitions": [{"node": "/dev/nvme0n1p3", "start": 2048, "size": 2048, "type": "8484680C-9521-48C6-9C11-B0720656F69E", "uuid": "AB", "name": "store_2ece03b0fa094b77"}]}}`
		parts, err := ParseTable("/dev/nvme0n1", []byte(out))
		if err != nil {
			t.Fatal(err)
		}
		if p := parts[0]; p.Bytes() != tc.want || p.Number != 3 || p.Type != "8484680c-9521-48c6-9c11-b0720656f69e" || p.UUID != "ab" {
			t.Errorf("%s: %+v has %d bytes, want %d", tc.sector, p, p.Bytes(), tc.want)
		}
	}
	if _, err := ParseTable("/dev/vda", []byte(`{"partitiontable": {"label": "dos"}}`)); err == nil || !strings.Contains(err.Error(), `a "dos" partition table, not a GPT`) {
		t.Errorf("a DOS table: %v", err)
	}
}

// TestHeaderArchitecture requires an architecture chalkos builds images for.
func TestHeaderArchitecture(t *testing.T) {
	img := newImage(t, "b", 3)
	for arch, ok := range map[string]bool{"x86-64": true, "arm64": true, "": false, "amd64": false} {
		h := img.header
		h.Architecture = arch
		if err := h.Validate(); (err == nil) != ok || err != nil && !strings.Contains(err.Error(), "architecture") {
			t.Errorf("Validate of %q = %v", arch, err)
		}
	}
}

// TestSkippedStoreIsChecked installs an image again into the slot that holds it, though no entry
// boots it any more: the store and hash tree are not written again, but their SHA-256 is checked
// all the same.
func TestSkippedStoreIsChecked(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	for _, tc := range []struct {
		name string
		edit func(h *Header)
	}{
		{"store", func(h *Header) { h.StoreSHA256 = sum(nil) }},
		{"hash tree", func(h *Header) { h.VeritySHA256 = sum(nil) }},
	} {
		l := newLab(t, old)
		if _, err := l.install(img); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(l.esp, linuxDir, "chalkos_2+3.efi"))
		h := img.header
		tc.edit(&h)
		clear(l.writes)
		if _, err := l.node.Install(context.Background(), h, img.stream()); err == nil || !strings.Contains(err.Error(), "the "+tc.name+"'s SHA-256") {
			t.Errorf("install = %v, want the %s's SHA-256 refused", err, tc.name)
		}
		if len(l.writes) != 0 {
			t.Errorf("wrote partitions %v", l.writes)
		}
	}
}

// TestInstallsAnotherVariant installs an image of another variant than the running one: a node
// may change its variant through an upgrade, and the check of its config against the image's
// schemas is what guards that.
func TestInstallsAnotherVariant(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	osRelease := img.osRelease(3)
	osRelease["CHALKOS_VARIANT"] = "gpu"
	img.uki = newUKI(osRelease, img.root)
	l := newLab(t, old)
	res, err := l.install(withHeader(img))
	if err != nil || res.AlreadyInstalled || res.Entry == "" {
		t.Fatalf("install = %+v, %v", res, err)
	}
}

// skipStream counts the bytes Skip passed over without reading them.
type skipStream struct {
	io.Reader
	skipped int64
}

func (s *skipStream) Skip(n int64) error { s.skipped += n; return nil }

// TestSkipPassesOverASkipper passes over the store and hash tree of a stream that can pass over
// bytes without reading them, as a pull does over layers it has not fetched.
func TestSkipPassesOverASkipper(t *testing.T) {
	s := &skipStream{Reader: strings.NewReader("uki")}
	if err := Skip(Header{StoreSize: 3, VeritySize: 2}, s); err != nil || s.skipped != 5 {
		t.Fatalf("Skip = %v, skipped %d", err, s.skipped)
	}
	if rest, _ := io.ReadAll(s); string(rest) != "uki" {
		t.Errorf("the stream goes on with %q", rest)
	}
}

// TestInstallPassesOverAHeldStoreOfAPull installs an image pulled from a registry again into the
// slot that holds it, though no entry boots it any more: the pull fetches the UKI alone, and the
// install writes no partition.
func TestInstallPassesOverAHeldStoreOfAPull(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(l.esp, linuxDir, "chalkos_2+3.efi"))

	var served sync.Map
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, digest, ok := strings.Cut(r.URL.Path, "/blobs/"); ok && r.Method == http.MethodGet {
			served.Store(digest, true)
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	oh := ociimage.Header{ImageID: "chalkos", RootHash: hex.EncodeToString(img.root), Architecture: "x86-64", Platform: "metal"}
	contract, err := ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}.Tar()
	if err != nil {
		t.Fatal(err)
	}
	oci, err := ociimage.Build(oh,
		ociimage.BytesBlob(ociimage.StoreType, img.store),
		ociimage.BytesBlob(ociimage.HashTreeType, img.hash),
		ociimage.BytesBlob(ociimage.UKIType, img.uki),
		ociimage.BytesBlob(ociimage.BootLoaderType, []byte("boot loader")),
		ociimage.BytesBlob(ociimage.ContractType, contract))
	if err != nil {
		t.Fatal(err)
	}
	ref := ocitest.Push(t, oci, strings.TrimPrefix(srv.URL, "http://")+"/lab/node-metal:v1")
	clear(l.writes)
	served.Clear()
	h := img.header
	pull, err := ociimage.NewPull(context.Background(), ref, []ociimage.Source{{Auth: authn.Anonymous, Insecure: true}}, oh, []ociimage.Part{
		{MediaType: ociimage.StoreType, Size: h.StoreSize, SHA256: h.StoreSHA256},
		{MediaType: ociimage.HashTreeType, Size: h.VeritySize, SHA256: h.VeritySHA256},
		{MediaType: ociimage.UKIType, Size: h.UKISize, SHA256: h.UKISHA256},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pull.Close()
	if res, err := l.node.Install(context.Background(), h, pull); err != nil || res.Entry == "" {
		t.Fatalf("install = %+v, %v", res, err)
	}
	if len(l.writes) != 0 {
		t.Errorf("wrote partitions %v", l.writes)
	}
	for what, data := range map[string][]byte{"store": img.store, "hash tree": img.hash} {
		if _, ok := served.Load("sha256:" + hex.EncodeToString(sum(data))); ok {
			t.Errorf("the %s was fetched", what)
		}
	}
	if _, ok := served.Load("sha256:" + hex.EncodeToString(sum(img.uki))); !ok {
		t.Error("the UKI was not fetched")
	}
}

// TestInstallRefusesAStreamGoingOnAfterTheUKI refuses a stream with bytes after the UKI.
func TestInstallRefusesAStreamGoingOnAfterTheUKI(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	stream := io.MultiReader(img.stream(), strings.NewReader("x"))
	if _, err := l.node.Install(context.Background(), img.header, stream); err == nil || !strings.Contains(err.Error(), "the stream goes on after the UKI") {
		t.Errorf("install = %v", err)
	}
}

// TestInstallRefusesAStreamFailingAtItsEnd refuses a stream whose read after the UKI fails.
func TestInstallRefusesAStreamFailingAtItsEnd(t *testing.T) {
	old, img := newImage(t, "a", 3), newImage(t, "b", 3)
	l := newLab(t, old)
	stream := io.MultiReader(img.stream(), iotest.ErrReader(errors.New("connection reset")))
	if _, err := l.node.Install(context.Background(), img.header, stream); err == nil || !strings.Contains(err.Error(), "receive the UKI: connection reset") {
		t.Errorf("install = %v", err)
	}
}
