package install

import (
	"context"
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storeslot"
)

// FromParts installs the node onto the target disk from the node's image's parts. Each step finds
// what an earlier, interrupted attempt did and continues from there:
//
//  1. Classify the target: an empty disk, or any with WipeDisk, is wiped and laid out anew; one
//     that holds what an earlier attempt left is continued, without the boot loader that attempt
//     may have written or BootNext; anything else is refused.
//  2. Lay out the system region with one systemd-repart run of the image's definitions: the ESP,
//     slot A, slot B as _empty, and STATE.
//  3. Write slot A with the slot writer an upgrade uses: retired, written, verified from the
//     disk, and activated with the UUIDs derived from the root hash and labels naming it.
//  4. Write the UKI to the ESP once it passed the upgrade's checks, then receive and check the
//     boot loader.
//  5. Open STATE, apply the storage section, enroll the fallback key and write the identity.
//  6. Write the boot loader, add the UEFI boot entry and boot the target next, and write the
//     installed marker last.
//
// Until the boot loader is written, firmware finds nothing to boot on the target, so an
// interrupted install never boots a node half installed.
func (i *Installer) FromParts(ctx context.Context, req PartsRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	h := req.Image
	if err := h.Validate(); err != nil {
		return fmt.Errorf("the image: %w", err)
	}
	if h.BootLoaderSize == 0 {
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
	// An install that a crashed chalkd left behind holds the target's STATE and its ESP.
	if err := i.closeState(ctx); err != nil {
		return err
	}
	if err := i.releaseESP(ctx); err != nil {
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
	if err := slots.WriteActive(ctx, pairs[0], h.Store(), req.Parts); err != nil {
		return err
	}
	espPart, err := i.findESP(ctx, target)
	if err != nil {
		return err
	}
	loader, err := i.writeUKI(ctx, espPart, h, req.Parts)
	if err != nil {
		return err
	}
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
