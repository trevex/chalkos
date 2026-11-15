package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// FromParts installs the node onto the target disk from the node's image's parts, continuing
// where an earlier, interrupted attempt stopped. Until the boot loader is written, firmware finds
// nothing to boot on the target, so an interrupted install never boots a node half installed. A
// config the image's checker refuses leaves the target so, with nothing of the node on STATE; an
// install of the image with config it accepts continues on the disk.
func (i *Installer) FromParts(ctx context.Context, req PartsRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	if req.Check == nil {
		return errors.New("the request has no check of the config by the image")
	}
	h := req.Image
	if err := h.Validate(); err != nil {
		return fmt.Errorf("the image: %w", err)
	}
	if h.BootLoader.Size == 0 {
		return errors.New("the image brings no boot loader, which an install writes to the ESP")
	}
	if i.loader() == "" {
		return errors.New("chalkos builds no images for this machine's architecture")
	}
	if h.Architecture != i.Architecture {
		return fmt.Errorf("the image is built for %s, this machine for %s", h.Architecture, i.Architecture)
	}
	l, err := parseLayout(req.SystemDefinitions, h.Architecture)
	if err != nil {
		return err
	}
	if err := l.fits(h); err != nil {
		return err
	}
	target, err := i.Host.Resolve(req.Target)
	if err != nil {
		return fmt.Errorf("resolve the target disk %s: %w", req.Target, err)
	}
	if err := i.refuseBootDisk(target); err != nil {
		return err
	}
	// An install that a crashed chalkd left behind holds the target's STATE, its ESP and the store
	// of slot A it checked.
	if err := i.closeState(ctx); err != nil {
		return err
	}
	if err := i.releaseESP(ctx); err != nil {
		return err
	}
	if err := i.releaseStore(ctx); err != nil {
		return err
	}
	// From here on the steps change the disk: a client that goes away must not kill a tool while
	// it writes. Its stream ends, which stops the install between steps.
	ctx = context.WithoutCancel(ctx)
	err = i.fromParts(ctx, target, l, req)
	if err != nil {
		// Leave the target free, so the install can run again.
		if cerr := i.closeState(ctx); cerr != nil {
			return fmt.Errorf("%w; releasing the target's STATE failed too: %v", err, cerr)
		}
	}
	return err
}

func (i *Installer) fromParts(ctx context.Context, target storage.BlockDisk, l layout, req PartsRequest) error {
	h := req.Image
	fresh, err := i.classify(ctx, target, l, req)
	if err != nil {
		return err
	}
	if fresh {
		if err := i.wipe(ctx, target); err != nil {
			return err
		}
		if err := i.layOut(ctx, target, req.SystemDefinitions, req.Section.Encryption); err != nil {
			return err
		}
	} else if err := i.unboot(ctx, target); err != nil {
		return err
	}
	slots := i.slotWriter(target)
	parts, err := slots.ReadTable(ctx)
	if err != nil {
		return err
	}
	pairs, err := storeslot.StoreSlots(target.String(), parts)
	if err != nil {
		return err
	}
	if err := slots.WriteActive(ctx, pairs[0], h.Image, req.Parts); err != nil {
		return err
	}
	espPart, err := i.findESP(ctx, target)
	if err != nil {
		return err
	}
	loader, system, err := i.writeUKI(ctx, espPart, h, req.Parts)
	if err != nil {
		return err
	}
	checked, err := i.checkConfig(ctx, pairs[0], system, req)
	if err != nil {
		return err
	}
	req.Config, req.Layers = checked.Config, checked.Layers
	if err := i.installOn(ctx, target, req.SystemDefinitions, req.Request, h.RootHash); err != nil {
		return err
	}
	if err := i.writeBootLoader(ctx, espPart, loader); err != nil {
		return err
	}
	if err := i.addBootEntry(ctx, target); err != nil {
		return err
	}
	return i.markInstalled()
}

// checkConfig mounts slot A's store through dm-verity and has the image's checker check the
// node's config with req.Check. The checker's identity must give the storage section the target
// was laid out by, as the installer laid it out with its own code before the image could check
// the config. Every refusal says that nothing boots the target; an installer built with the image
// may lay the target out otherwise and need --wipe-disk.
func (i *Installer) checkConfig(ctx context.Context, slot storeslot.Slot, system string, req PartsRequest) (Checked, error) {
	const continues = "the target disk boots nothing, and an install of the image continues on it"
	var checked Checked
	err := upgrade.OpenStore(ctx, i.Ops, i.storeDir(), req.Image.RootHash, slot, system, func(st upgrade.Store) error {
		var err error
		checked, err = req.Check(ctx, st, st.Toplevel)
		return err
	})
	if err != nil {
		return Checked{}, fmt.Errorf("%w; %s", err, continues)
	}
	if len(checked.Config.Identity) == 0 {
		return Checked{}, fmt.Errorf("the image's checker gave no identity; %s", continues)
	}
	var id identity.Identity
	if err := json.Unmarshal(checked.Config.Identity, &id); err != nil {
		return Checked{}, fmt.Errorf("parse the identity the image's checker gave: %w; %s", err, continues)
	}
	if diff := sectionDifference(req.Section, id.Storage); len(diff) > 0 {
		return Checked{}, fmt.Errorf("the image translates the config's storage otherwise than the installer, which laid the target disk out by its own translation: they differ in %s; the target disk boots nothing; install the image with the installer built with it, with --wipe-disk if that installer refuses the disk's layout", strings.Join(diff, ", "))
	}
	return checked, nil
}

// sectionDifference names the parts in which the storage sections differ: the encryption policy,
// the fallback, and disks and volumes by name. A part missing from one section and empty in the
// other is no difference.
func sectionDifference(a, b storage.Section) []string {
	var diff []string
	if a.Encryption != b.Encryption {
		diff = append(diff, "the encryption policy")
	}
	if a.Fallback != b.Fallback {
		diff = append(diff, "the fallback")
	}
	for _, name := range slices.Sorted(maps.Keys(union(a.Disks, b.Disks))) {
		da, oka := a.Disks[name]
		db, okb := b.Disks[name]
		if oka != okb || da.Ref != db.Ref || da.Seed != db.Seed || !maps.Equal(da.Repart, db.Repart) {
			diff = append(diff, "disk "+name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(union(a.Volumes, b.Volumes))) {
		va, oka := a.Volumes[name]
		vb, okb := b.Volumes[name]
		if oka != okb || va != vb {
			diff = append(diff, "volume "+name)
		}
	}
	return diff
}

// union holds the keys of both maps.
func union[V any](a, b map[string]V) map[string]bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	return keys
}

// storeDir is where the store of slot A is mounted while the image's checker checks the config.
func (i *Installer) storeDir() string { return filepath.Join(i.WorkDir, "store") }

// releaseStore unmounts the store of slot A and closes its dm-verity device where a crashed chalkd
// left them during a check, which keep the target in use.
func (i *Installer) releaseStore(ctx context.Context) error {
	return hostops.Release(ctx, i.Ops, "the checked store", i.storeDir(), upgrade.StoreMapping, i.Ops.CloseVerity)
}
