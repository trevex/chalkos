package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
)

// Previous is the entry a rollback boots: of the image's UKIs below the booted entry's generation,
// the highest whose tries are not used up and that boots another store than the running one. An
// upgrade removes such a UKI before it writes the inactive slot, so the slot still holds its
// store; Rollback checks all the same. It is false without one, as after an install or a fallback,
// and when the booted entry's name gives no generation.
func Previous(entries []esp.Entry, imageID string, booted esp.Entry, running []byte) (esp.Entry, bool) {
	top := esp.EntryGeneration(booted.ID, imageID)
	var prev esp.Entry
	var generation uint64
	for _, e := range entries {
		g := esp.EntryGeneration(e.ID, imageID)
		if g == 0 || g >= top || e.ImageID != imageID || e.Bad() || e.RootHash == nil || bytes.Equal(e.RootHash, running) {
			continue
		}
		if g > generation {
			prev, generation = e, g
		}
	}
	return prev, generation > 0
}

// Rollback makes the image Previous names, by its root hash, boot next again; the inactive slot
// must still hold its store. It checks the UKI as an upgrade checks a new one and renames it to
// the image's next generation. While the running image's boot was found healthy, the name carries
// the UKI's tries, so systemd-boot falls back to the running image should they fail. Before that,
// as when leaving the upgrade that replaced the image, it carries none: the image, found healthy
// before, booting with the config slot it last ran, stays the known-good entry, where a counted
// one could leave only entries never found healthy.
//
// The rename is the only change, so a rollback stopped before it changed nothing. The caller
// keeps Install from running meanwhile, as Install retires the slot. last is the root hash of the
// last rollback's image as the caller recorded it, nil without one. record, when set, is called
// once every check passed, just before the rename, or when the image boots next already; its
// error stops the rollback. A node that runs the image answers AlreadyInstalled; one that boots it
// next already, from an entry without tries or of last, AlreadyStaged. Any other staged entry,
// an upgrade's even of this image, replaced the image before, and Rollback refuses.
func (n *Node) Rollback(ctx context.Context, rootHash, last []byte, record func() error) (Result, error) {
	if record == nil {
		record = func() error { return nil }
	}
	running, err := uki.RunningHash(n.Cmdline)
	if err != nil {
		return Result{}, err
	}
	if bytes.Equal(rootHash, running) {
		return Result{AlreadyInstalled: true}, nil
	}
	imageID, all, booted, err := n.bootState(running)
	if err != nil {
		return Result{}, err
	}
	if staged, ok := esp.StagedEntry(all, imageID, booted); ok {
		switch {
		case bytes.Equal(staged.RootHash, rootHash) && (staged.TriesLeft < 0 || bytes.Equal(last, rootHash)):
			if err := record(); err != nil {
				return Result{}, err
			}
			return Result{AlreadyStaged: true, Entry: staged.File, Active: true}, nil
		// An upgrade counts the tries of the entry it stages. One without tries is the image a
		// rollback from a boot not found healthy staged, or a blessed entry above the booted one,
		// as when the boot menu picked an entry below it.
		case staged.TriesLeft < 0:
			return Result{}, fmt.Errorf("the image %x is staged to boot next from %s", staged.RootHash, staged.File)
		}
		return Result{}, fmt.Errorf("the image %x is staged to boot next from %s; its upgrade replaced the image the node ran before", staged.RootHash, staged.File)
	}
	// An image staged and not active yet replaced the image before too: the upgrade that staged it
	// retired that image's UKI and wrote over the slot.
	if staged, ok, err := esp.Staged(n.ESP, imageID); err != nil {
		return Result{}, err
	} else if ok {
		return Result{}, fmt.Errorf("the image %x is staged in the inactive slot; its upgrade replaced the image the node ran before", staged.RootHash)
	}
	prev, ok := Previous(all, imageID, booted, running)
	switch {
	case !ok:
		return Result{}, errors.New("the node keeps no image it ran before to roll back to")
	case !bytes.Equal(prev.RootHash, rootHash):
		return Result{}, fmt.Errorf("the image the node ran before is %x, not %x", prev.RootHash, rootHash)
	}
	// The renamed entry boots before the others only while their names order them by generation,
	// as for an upgrade's new entry.
	if err := checkNames(all, imageID, booted, nil); err != nil {
		return Result{}, err
	}
	inactive, err := n.inactiveSlot(ctx, running)
	if err != nil {
		return Result{}, err
	}
	if !inactive.Holds(rootHash) {
		return Result{}, fmt.Errorf("the inactive slot no longer holds the store of %x, which %s boots", rootHash, prev.File)
	}
	path := filepath.Join(n.ESP, esp.LinuxDir, prev.File)
	tries, err := n.checkKeptUKI(path, imageID, rootHash)
	if err != nil {
		return Result{}, err
	}
	generation, err := esp.NextGeneration(all, imageID)
	if err != nil {
		return Result{}, err
	}
	name := esp.CountedEntryName(imageID, generation, tries)
	if booted.TriesLeft >= 0 {
		name = esp.EntryName(imageID, generation)
	}
	if err := record(); err != nil {
		return Result{}, err
	}
	if err := esp.Name(n.ESP, path, name, n.Change); err != nil {
		return Result{}, err
	}
	log.Printf("%s %x boots next again as %s", imageID, rootHash, name)
	return Result{Entry: name, Active: true}, nil
}

// checkKeptUKI checks the UKI at path, of the image a rollback or an activation boots, as CheckUKI
// checks an upgrade's, and returns the tries it asks for. db and dbx may have changed since the
// install, so the signature is checked again. A refusal is a RefusalError; a failure to read the
// UKI or the firmware's variables is not.
func (n *Node) checkKeptUKI(path, imageID string, rootHash []byte) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	h := Header{ImageID: imageID, Platform: n.OSRelease["CHALKOS_PLATFORM"], Architecture: n.Architecture, Image: storeslot.Image{RootHash: rootHash}, UKI: imagepart.Part{Size: info.Size()}}
	tries, err := CheckUKI(f, h, n.EFIVars)
	if _, ok := errors.AsType[*UKIError](err); ok {
		return 0, refused(err)
	}
	return tries, err
}

// KnownGood reports whether a rollback from a boot not found healthy staged the known-good image:
// uncounted while the booted entry is counted. The node falls back to it, with the config it last
// ran, until the boot is found healthy.
func KnownGood(staged, booted esp.Entry) bool {
	return staged.TriesLeft < 0 && booted.TriesLeft >= 0
}
