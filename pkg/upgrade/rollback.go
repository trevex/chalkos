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
	"github.com/trevex/chalkos/pkg/uki"
)

// Previous is the entry of the image a rollback boots: of the image's UKIs whose names give a
// generation below the booted entry's, the highest whose tries are not used up and which boots
// another store than the running one. An upgrade removes such a UKI before it writes the inactive
// slot, so while it is on the ESP the slot still holds its store; Rollback checks the slot all the
// same. false without one, as after an install or a fallback, and when the booted entry's name
// gives no generation.
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

// Rollback makes the image Previous names, whose store has the root hash, boot next again. Nothing
// is written to the inactive slot, which must still hold the store. The image's UKI is checked as
// an upgrade checks a new one, then renamed to the image's next generation with the tries it asks
// for: systemd-boot counts its boots and falls back to the running image should they fail, as
// after an upgrade. The rename is the only change, so until it the running image boots next; a
// rollback stopped before it changed nothing, and one asked for again after it finds the image
// staged. The caller keeps Install from running meanwhile, as Install retires the slot.
//   - A node that runs the image answers AlreadyInstalled, and one that boots it next already
//     AlreadyStaged, so a rollback asked for twice happens once.
//   - Another image staged to boot next replaced the image before, as an upgrade's retirement
//     removed its UKI; Rollback refuses.
func (n *Node) Rollback(ctx context.Context, rootHash []byte) (Result, error) {
	running, err := uki.RunningHash(n.Cmdline)
	if err != nil {
		return Result{}, err
	}
	if bytes.Equal(rootHash, running) {
		return Result{AlreadyInstalled: true}, nil
	}
	imageID := n.OSRelease["IMAGE_ID"]
	if imageID == "" {
		return Result{}, errors.New("the running image names no image ID")
	}
	all, err := esp.Entries(n.ESP)
	if err != nil {
		return Result{}, err
	}
	booted, err := Booted(all, n.EFIVars, running)
	if err != nil {
		return Result{}, err
	}
	if staged, ok := esp.StagedEntry(all, imageID, booted); ok {
		if bytes.Equal(staged.RootHash, rootHash) {
			return Result{AlreadyStaged: true, Entry: staged.File}, nil
		}
		return Result{}, fmt.Errorf("the image %x is staged to boot next from %s; its upgrade replaced the image the node ran before", staged.RootHash, staged.File)
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
	for _, e := range all {
		if !ofImage(e, imageID) && e.ID != booted.ID {
			continue
		}
		if err := misnamed(e, imageID); err != nil {
			return Result{}, err
		}
	}
	parts, err := n.slots().ReadTable(ctx)
	if err != nil {
		return Result{}, err
	}
	_, inactive, err := slots(parts, running)
	if err != nil {
		return Result{}, err
	}
	if !inactive.Holds(rootHash) {
		return Result{}, fmt.Errorf("the inactive slot no longer holds the store of %x, which %s boots", rootHash, prev.File)
	}
	path := filepath.Join(n.ESP, esp.LinuxDir, prev.File)
	tries, err := n.checkPreviousUKI(path, imageID, rootHash)
	if err != nil {
		return Result{}, err
	}
	generation, err := esp.NextGeneration(all, imageID)
	if err != nil {
		return Result{}, err
	}
	name := esp.CountedEntryName(imageID, generation, tries)
	if err := esp.Name(n.ESP, path, name, n.Change); err != nil {
		return Result{}, err
	}
	log.Printf("%s %x boots next again as %s", imageID, rootHash, name)
	return Result{Entry: name}, nil
}

// checkPreviousUKI checks the UKI at path of the image a rollback boots as CheckUKI checks an
// upgrade's, against the node's image ID, platform and architecture and the image's root hash,
// and returns the tries it asks for. Firmware's db and dbx may have changed since the UKI was
// installed, so its signature is checked again.
func (n *Node) checkPreviousUKI(path, imageID string, rootHash []byte) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	h := Header{ImageID: imageID, Platform: n.OSRelease["CHALKOS_PLATFORM"], Architecture: n.Architecture, RootHash: rootHash, UKISize: info.Size()}
	return CheckUKI(f, h, n.EFIVars)
}
