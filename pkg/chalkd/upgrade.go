package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Upgrade installs the streamed or pulled image with its config, stages it and activates it
// unless the header's stage_only stops there. It checks the image's platform, that it boots its
// store and that its own checker accepts the config, but cannot tell an image to trust: without
// Secure Boot an operator can run any image as root, with STATE and VAR unsealed, as their keys
// are sealed to PCR 7 alone. Secure Boot limits upgrades to images signed for db.
//
// Config or a fallback secret needs the admin role, as ApplyConfig does: what an operator may not
// deliver to the running image must not reach the next one either. Without config the image gets
// the pending config, else the running image's. A pulled image is fetched by the header's digest
// with the credentials of the config's Registry of its host.
func (s *Server) Upgrade(ctx context.Context, stream *connect.ClientStream[nodev1.UpgradeRequest]) (*connect.Response[nodev1.UpgradeResponse], error) {
	unlock, err := s.lockSlots("an upgrade")
	if err != nil {
		return nil, err
	}
	defer unlock()
	h, err := readHeader(stream, (*nodev1.UpgradeRequest).GetHeader, "upgrade")
	if err != nil {
		return nil, err
	}
	if role, _ := ctx.Value(roleKey{}).(pki.Role); (h.GetConfig() != "" || h.GetFallbackSecret() != "") && !role.Allows(pki.RoleAdmin) {
		return nil, layer.Errorf(connect.CodePermissionDenied, "%s with config or a fallback secret needs the %s role; without them the node gives the image its pending config, else carries its own config over", stream.Spec().Procedure, pki.RoleAdmin)
	}
	img := h.GetImage()
	switch {
	case img == nil:
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the header names no image")
	case img.GetBootLoader() != nil:
		return nil, layer.Errorf(connect.CodeInvalidArgument, "an upgrade carries no boot loader: it leaves the node's as it is")
	case h.GetStageOnly() && h.GetReboot():
		return nil, layer.Errorf(connect.CodeInvalidArgument, "an upgrade that stops once the image is staged does not reboot: the node boots a staged image only once ActivateImage activated it")
	}
	header := imageHeader(img)
	// The node's known-good image, staged by a rollback, keeps the config it last ran. Only config
	// is refused for it, so an upgrade without config reads the ESP for it once Install found the
	// image staged.
	knownGood := func() (bool, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.keepsKnownGood(img.GetRootHash(), h.GetConfig())
	}
	var keepsConfig bool
	if h.GetConfig() != "" {
		var err error
		if keepsConfig, err = knownGood(); err != nil {
			return nil, err
		}
	}
	parts := chunks(stream)
	if src := h.GetSource(); src != nil {
		ref := src.GetOci().GetReference()
		if ref == "" {
			return nil, layer.Errorf(connect.CodeInvalidArgument, "the header's source names no image in a registry")
		}
		// The parts come from the registry.
		if err := noChunks(stream, "an upgrade"); err != nil {
			return nil, err
		}
		pull, err := s.pullParts(ctx, ref, header, h.GetConfig())
		if err != nil {
			return nil, fmt.Errorf("upgrade: %w", err)
		}
		defer pull.Close()
		parts = pull
		log.Printf("upgrading to %q with the root hash %x, pulled from %s", img.GetImageId(), img.GetRootHash(), ref)
	} else {
		log.Printf("upgrading to %q with the root hash %x", img.GetImageId(), img.GetRootHash())
	}
	res, staged, err := s.imageSlots().Install(ctx, header, h.GetConfig(), h.GetFallbackSecret(), h.GetStageOnly(), parts)
	if _, ok := errors.AsType[*upgrade.StagedError](err); ok {
		log.Printf("upgrade to %x failed: %v", img.GetRootHash(), err)
		// The image is staged with its config, as by a stage-only upgrade, and ActivateImage may
		// activate it later: record its provenance, forget the rollback to the image it replaced,
		// and drop the pending config its slot took or replaced. The error names no pending
		// config; Status does.
		s.recordImages(&header)
		s.forgetRollback()
		s.settlePending(staged, img.GetRootHash())
		// An activation that refused the staged UKI fails the precondition, and one that failed to
		// read or change the disk or the ESP is internal, as ActivateImage answers them: see
		// domainCodes.
		return nil, fmt.Errorf("upgrade: %w", err)
	}
	if err != nil {
		log.Printf("upgrade to %x failed: %v", img.GetRootHash(), err)
		// A refusal retired the image, so its config slot goes too.
		s.recordImages(nil)
		// An image staged already is sent again for its config alone, and a refusal of that config
		// in the recheck retires the image with it; config apply --staged would have refused it and
		// kept the image. A recheck that failed to mount or read the store retired the image too,
		// but refused no config, so the answer names no config apply --staged. Other failures, also
		// of a resend, came before the recheck or wrote the image anew, and name no staged image.
		if retired, ok := errors.AsType[*upgrade.StagedRetiredError](err); ok {
			if _, ok := errors.AsType[*upgrade.CheckError](retired.Err); !ok {
				return nil, layer.Errorf(connect.CodeFailedPrecondition, "upgrade: %w; the staged image was retired with it", err)
			}
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "upgrade: %w; the staged image was retired with it: chalkctl config apply --staged %s changes a staged image's config without retiring the image on a refusal", err, s.nodeName())
		}
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "upgrade: %w", err)
	}
	s.recordImages(&header)
	// The image is staged, or boots next, with the config stageConfig gave it, so the pending config
	// it took or that the upgrade's config replaced goes. An image the node runs already or the
	// known-good image kept with its config never had its slot written, and the pending config
	// stays.
	pending := s.settlePending(staged, img.GetRootHash())
	resp := &nodev1.UpgradeResponse{AlreadyInstalled: res.AlreadyInstalled, AlreadyStaged: res.AlreadyStaged, Entry: res.Entry, Active: res.Active, Pending: pending}
	if res.AlreadyInstalled {
		log.Printf("%x runs already", img.GetRootHash())
		return connect.NewResponse(resp), nil
	}
	if res.AlreadyStaged && h.GetConfig() == "" {
		if keepsConfig, err = knownGood(); err != nil {
			return nil, err
		}
	}
	switch {
	case res.AlreadyStaged && keepsConfig:
		// Nothing changed, so the record of the rollback that staged the image stays.
		log.Printf("%x boots next already, as %s, with the config it last ran", img.GetRootHash(), res.Entry)
	case res.AlreadyStaged:
		s.forgetRollback()
		log.Printf("%x was in the inactive slot already, as %s; its config is replaced", img.GetRootHash(), res.Entry)
	default:
		// The upgrade replaced how the node got to the image it stages; one to the image it runs
		// already leaves the record of a rollback to that image.
		s.forgetRollback()
	}
	if !res.Active {
		log.Printf("%x is staged as %s; ActivateImage makes it boot next", img.GetRootHash(), res.Entry)
	}
	if h.Reboot {
		log.Printf("rebooting into %x", img.GetRootHash())
		resp.Rebooting = true
		s.rebootSoon()
	}
	return connect.NewResponse(resp), nil
}

// stageConfig writes the config slot of the image an upgrade installs, in the store st with the
// root hash, through writeSlot: the config text delivers, else the node's pending config, else the
// running slot's documents as delivered. secret is the fallback secret new encrypted volumes are
// enrolled with. The config it returns names the pending config the image took or replaced, which
// Upgrade drops only once the image is staged, and activated unless the upgrade stops at staging;
// a refusal keeps it. It runs once the store is in the inactive slot, before the image is staged,
// and again for an image the slot holds already.
func (s *Server) stageConfig(ctx context.Context, st imageStore, system, platform string, rootHash []byte, text, secret string) (imageConfig, error) {
	// ApplyConfig writes the slots and changes the machine under the same lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.upgradeConfig(text)
	if err != nil {
		return imageConfig{}, err
	}
	warnings, err := s.writeSlot(ctx, st, system, platform, rootHash, c.docs, secret)
	if err != nil {
		return imageConfig{}, c.refused(err, s.nodeName())
	}
	logWarnings(rootHash, warnings)
	c.docs = nil
	return c, nil
}

// slotWrite is a config slot of an image that does not run, checked and ready to be written: the
// slot, the changes its storage makes to the machine, and the warnings of the image's check.
type slotWrite struct {
	next     slot.Stored
	machine  machineChange
	warnings []string
}

// planSlot checks docs, config as delivered, for the config slot of an image that does not run,
// in the store st with the root hash, and changes nothing. The image checks the config with its
// own chalkd, as it would once it runs: its schemas, rules and renderers, against the running
// slot's config and with its own facts of this node. chalkd then checks the slot's storage against
// the machine. Config chalkos-config could not apply, which would leave boot counting to fall
// back, is refused, and so are storage changes that take a reset or a reinstall. recorded, when
// set, is the storage a delivery to the running image records first, which the image checks
// against and plans its changes on top of; nil plans on what STATE records. The caller holds s.mu.
func (s *Server) planSlot(ctx context.Context, st imageStore, system, platform string, rootHash []byte, docs []config.Document, secret string, recorded *storage.Section) (slotWrite, error) {
	_, running, _, err := s.runningSlot()
	if err != nil {
		return slotWrite{}, err
	}
	name, err := s.name()
	if err != nil {
		return slotWrite{}, err
	}
	next, warnings, err := s.checkWithImage(ctx, st, system, platform, name, imageCheck{delivered: docs, old: running.Delivered, secret: secret, recorded: recorded, image: rootHash, writing: rootHash})
	if err != nil {
		return slotWrite{}, err
	}
	d, err := s.parseIdentity(string(next.Identity))
	if err != nil {
		return slotWrite{}, err
	}
	var mc machineChange
	if recorded != nil {
		mc = machineChange{recorded: *recorded, section: d.section, changes: storage.Changes(*recorded, d.section)}
	} else if mc, err = s.machineChanges(d.section); err != nil {
		return slotWrite{}, err
	}
	return slotWrite{next: next, machine: mc, warnings: warnings}, nil
}

// writeSlot writes docs to the config slot of an image that does not run once planSlot passed
// them, and returns the warnings of the image's check. Storage changes the image accepts, such as a
// new volume, are made now and recorded in storage.json. Every boot sets up what storage.json
// records, so the running image mounts such a volume too; its slot removes the volume only once
// it is written again. The caller holds s.mu.
func (s *Server) writeSlot(ctx context.Context, st imageStore, system, platform string, rootHash []byte, docs []config.Document, secret string) ([]string, error) {
	w, err := s.planSlot(ctx, st, system, platform, rootHash, docs, secret, nil)
	if err != nil {
		return nil, err
	}
	if err := s.applyMachine(ctx, w.machine, secret); err != nil {
		return nil, err
	}
	if err := slot.Write(slot.Dir(s.Loader.Slots, rootHash), w.next); err != nil {
		return nil, fmt.Errorf("store the image's config: %w", err)
	}
	log.Printf("the config %s of %x is stored", w.next.Version, rootHash)
	return w.warnings, nil
}

// imageHeader is the image an install or upgrade header describes.
func imageHeader(h *nodev1.ImageHeader) upgrade.Header {
	return upgrade.Header{
		ImageID: h.GetImageId(), Platform: h.GetPlatform(), Architecture: h.GetArchitecture(),
		Image: storeslot.Image{RootHash: h.GetRootHash(), Store: headerPart(h.GetStore()), Verity: headerPart(h.GetHashTree())},
		UKI:   headerPart(h.GetUki()), BootLoader: headerPart(h.GetBootLoader()),
		Provenance: upgrade.Provenance{Revision: h.GetRevision(), Tag: h.GetTag()},
	}
}

// headerPart is the part of an image a header's part describes.
func headerPart(p *nodev1.ImagePart) imagepart.Part {
	return imagepart.Part{Size: int64(p.GetSize()), SHA256: p.GetSha256()}
}

// recordImages forgets, in one step, every image no UKI on the ESP boots any more, an entry's or
// the staged one: the record of installed images keeps only the others, and the config slots
// those of the others and of the running image. installed, when set, is the image an upgrade
// installed or found installed; the record keeps its provenance. A failure fails no upgrade: both
// only shrink, a boot loads only its own image's slot, and the next upgrade forgets again.
func (s *Server) recordImages(installed *upgrade.Header) {
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		log.Printf("forget the images the ESP no longer boots: %v", err)
		return
	}
	// The image staged and not active yet boots from no entry, but its activation boots it with
	// its slot.
	staged, ok, err := esp.Staged(s.Paths.ESP, readOSRelease(s.Paths.OSRelease)["IMAGE_ID"])
	if err != nil {
		log.Printf("forget the images the ESP no longer boots: %v", err)
		return
	}
	if ok {
		entries = append(entries, staged)
	}
	var keep [][]byte
	for _, e := range entries {
		if e.RootHash != nil {
			keep = append(keep, e.RootHash)
		}
	}
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		log.Printf("keep the running image's config: %v", err)
		return
	}
	s.mu.Lock()
	err = slot.Retire(s.Loader.Slots, append(keep, running))
	s.mu.Unlock()
	if err != nil {
		log.Printf("retire the config slots of images the ESP no longer boots: %v", err)
	}
	path := filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)
	images := upgrade.ReadImages(path)
	if installed != nil {
		images = images.Record(installed.RootHash, installed.Provenance, keep)
	} else {
		images = images.Keep(keep)
	}
	data, err := images.Encode()
	if err == nil {
		err = atomicfile.WriteFile(path, data, 0o644)
	}
	if err != nil {
		log.Printf("record the image's provenance: %v", err)
	}
}

// ActivateImage makes the staged image boot next, under the lock an upgrade and a rollback hold,
// so only one writes the ESP at a time. It checks no config: the image's checker passed its slot
// before it was staged. A staged image whose slot STATE no longer keeps is refused first, as
// checkStagedSlot says. Refusals fail the precondition; a failure to read or change the disk or
// the ESP is internal.
func (s *Server) ActivateImage(ctx context.Context, req *connect.Request[nodev1.ActivateImageRequest]) (*connect.Response[nodev1.ActivateImageResponse], error) {
	root := req.Msg.RootHash
	if len(root) == 0 {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the request names no image to activate")
	}
	unlock, err := s.lockSlots("an image's activation")
	if err != nil {
		return nil, err
	}
	defer unlock()
	res, err := s.imageSlots().Activate(ctx, root)
	if err != nil {
		log.Printf("activation of %x failed: %v", root, err)
		return nil, fmt.Errorf("activate: %w", err)
	}
	switch {
	case res.AlreadyInstalled:
		log.Printf("%x runs already", root)
	case res.AlreadyStaged:
		log.Printf("%x boots next already, as %s", root, res.Entry)
	default:
		log.Printf("activated %x, which boots next as %s", root, res.Entry)
	}
	return connect.NewResponse(&nodev1.ActivateImageResponse{Entry: res.Entry, AlreadyActive: res.AlreadyStaged, AlreadyInstalled: res.AlreadyInstalled}), nil
}

// checkStagedSlot refuses to activate the staged image, by its root hash, while STATE keeps no
// config slot for it: the image would boot without config, fail its health check and fall back.
func (s *Server) checkStagedSlot(rootHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keepsSlot(rootHash) {
		return fmt.Errorf("STATE holds no config for the image %x, which an upgrade stages with its config; it would boot without config", rootHash)
	}
	return nil
}
