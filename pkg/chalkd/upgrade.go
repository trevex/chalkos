package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"runtime"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Upgrade installs the image an operator streams with the node's config for it, stages it and,
// unless the header asks to stop there, activates it as ActivateImage does. It checks that the
// image is of the node's platform, whatever its variant, that it boots the store it carries and,
// before it is staged, that the image's own checker accepts the config, but cannot tell an image
// to trust from another: without Secure Boot, an operator can run any image as root, with STATE
// and VAR unsealed, as their keys are sealed to PCR 7 alone. Secure Boot is what limits upgrades
// to images signed for db.
//
// An operator may upgrade, but only an admin may deliver config with the upgrade, as ApplyConfig
// needs: config names the node's Addons, networks and volumes, and what an operator may not
// deliver to the running image it must not deliver to the next one either. Without config the
// node gives the image its pending config, which an admin's apply-config --staged held, or else
// carries the running image's over.
//
// An image the header names in a registry is pulled by the digest it names, with the credentials
// of the config's Registry of its host, and checked against the header as a streamed one.
func (s *Server) Upgrade(ctx context.Context, stream *connect.ClientStream[nodev1.UpgradeRequest]) (*connect.Response[nodev1.UpgradeResponse], error) {
	if !s.upgrading.TryLock() {
		return nil, failed(connect.CodeAborted, "an upgrade, an image rollback, a config delivery or the rollback at a trial's deadline is running")
	}
	defer s.upgrading.Unlock()
	if err := s.refuseOnTrialLocking("an upgrade"); err != nil {
		return nil, err
	}
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, failed(connect.CodeInvalidArgument, "the upgrade request has no header")
	}
	h := stream.Msg().GetHeader()
	if h == nil {
		return nil, failed(connect.CodeInvalidArgument, "the first upgrade message must be the header")
	}
	if role, _ := ctx.Value(roleKey{}).(pki.Role); (h.GetConfig() != "" || h.GetFallbackSecret() != "") && !role.Allows(pki.RoleAdmin) {
		return nil, failed(connect.CodePermissionDenied, "%s with config or a fallback secret needs the %s role; without them the node gives the image its pending config, else carries its own config over", stream.Spec().Procedure, pki.RoleAdmin)
	}
	img := h.GetImage()
	switch {
	case img == nil:
		return nil, failed(connect.CodeInvalidArgument, "the header names no image")
	case img.GetBootLoader() != nil:
		return nil, failed(connect.CodeInvalidArgument, "an upgrade carries no boot loader: it leaves the node's as it is")
	case h.GetStageOnly() && h.GetReboot():
		return nil, failed(connect.CodeInvalidArgument, "an upgrade that stops once the image is staged does not reboot: the node boots a staged image only once ActivateImage activated it")
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
			return nil, failed(connect.CodeInvalidArgument, "the header's source names no image in a registry")
		}
		// The parts come from the registry.
		if err := noChunks(stream, "an upgrade"); err != nil {
			return nil, err
		}
		pull, err := s.pullParts(ctx, ref, header, h.GetConfig())
		if err != nil {
			code := connect.CodeInvalidArgument
			var ce *connect.Error
			if errors.As(err, &ce) {
				code = ce.Code()
			}
			return nil, failed(code, "upgrade: %v", plain(err))
		}
		defer pull.Close()
		parts = pull
		log.Printf("upgrading to %q with the root hash %x, pulled from %s", img.GetImageId(), img.GetRootHash(), ref)
	} else {
		log.Printf("upgrading to %q with the root hash %x", img.GetImageId(), img.GetRootHash())
	}
	res, staged, err := s.installImage(ctx, header, h.GetConfig(), h.GetFallbackSecret(), h.GetStageOnly(), parts)
	if unactivated := (*upgrade.StagedError)(nil); errors.As(err, &unactivated) {
		log.Printf("upgrade to %x failed: %v", img.GetRootHash(), err)
		// The image is staged with the config stageConfig wrote, as after an upgrade that stops
		// there, and ActivateImage may activate it later: the node records its provenance, how the
		// node got to the image the inactive slot held before is gone with that image, and the
		// pending config goes as once a stage-only upgrade staged the image, since the image's slot
		// holds the config that took or replaced it. The answer is an error, which names no pending
		// config; Status tells.
		s.recordImages(&header)
		s.forgetRollback()
		s.settlePending(staged, img.GetRootHash())
		// An activation that refused the staged UKI fails the precondition, and one that failed to
		// read or change the disk or the ESP is internal, as ActivateImage answers them.
		code := connect.CodeInternal
		if refusal := (*upgrade.RefusalError)(nil); errors.As(err, &refusal) {
			code = connect.CodeFailedPrecondition
		}
		return nil, failed(code, "upgrade: %v", err)
	}
	if err != nil {
		log.Printf("upgrade to %x failed: %v", img.GetRootHash(), err)
		// A refusal retired the image, so its config slot goes too.
		s.recordImages(nil)
		// An image staged already is sent again for its config alone, and a refusal of that config
		// in the recheck retires the image with it; apply-config --staged would have refused it and
		// kept the image. A recheck that failed to mount or read the store retired the image too,
		// but refused no config, so the answer names no apply-config --staged. Other failures, also
		// of a resend, came before the recheck or wrote the image anew, and name no staged image.
		var retired *upgrade.StagedRetiredError
		if errors.As(err, &retired) {
			var check *upgrade.CheckError
			if !errors.As(retired.Err, &check) {
				return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v; the staged image was retired with it", err)
			}
			return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v; the staged image was retired with it: chalkctl apply-config --staged %s changes a staged image's config without retiring the image on a refusal", err, s.nodeName())
		}
		return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v", err)
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

// stageConfig writes the config slot of the image an upgrade installs, whose store is st with the
// root hash, through writeSlot. The config is the one text delivers, or else the node's pending
// config, or else the running slot's documents as they were delivered, as upgradeConfig chooses.
// secret is the fallback secret a new encrypted volume is enrolled with. It returns the config it
// wrote, without its documents, which names the pending config the image took or the upgrade's
// config replaced. Upgrade drops that once the image is staged, and activated unless the upgrade
// stops at staging: until then the image may still fail to be staged or to become the next boot,
// and the pending config waits for the upgrade after. A refusal keeps it, for apply-config
// --staged to replace. It runs once the store is written to the inactive slot, before the image
// is staged, and again for an image the slot holds already, staged or active.
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
	// An upgrade's answer has no warnings; the journal keeps them.
	for _, w := range warnings {
		log.Printf("the config of %x: %s", rootHash, w)
	}
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
// whose store is st with the root hash, and changes nothing. system is the image's NixOS system in
// st, platform its platform and secret the fallback secret. The caller holds s.mu.
//
// The image checks the config itself, with its own chalkd, as it would check a delivery once it
// runs: its schemas, its rules and its renderers, against the running slot's config and with its
// own facts of this node. chalkd then checks the slot's storage against the machine, as for every
// slot write. A config chalkos-config could not apply would leave boot counting to fall back.
// planSlot refuses:
//   - config the image refuses, such as one that changes what a node holding a share was set up
//     for, or one a renderer of the image cannot render;
//   - storage changes that take a reset or a reinstall, which the image's storage rules refuse.
func (s *Server) planSlot(ctx context.Context, st imageStore, system, platform string, rootHash []byte, docs []config.Document, secret string) (slotWrite, error) {
	_, running, _, err := s.runningSlot()
	if err != nil {
		return slotWrite{}, err
	}
	name, err := s.name()
	if err != nil {
		return slotWrite{}, err
	}
	next, warnings, err := s.checkWithImage(ctx, st, system, platform, name, imageCheck{delivered: docs, old: running.Documents, secret: secret, image: rootHash, writing: rootHash})
	if err != nil {
		return slotWrite{}, err
	}
	d, err := s.parseIdentity(string(next.Identity))
	if err != nil {
		return slotWrite{}, err
	}
	mc, err := s.machineChanges(d.section)
	if err != nil {
		return slotWrite{}, err
	}
	return slotWrite{next: next, machine: mc, warnings: warnings}, nil
}

// writeSlot writes docs to the config slot of an image that does not run once planSlot checked
// them. Storage changes the image's rules accept, such as a new volume, are made now and recorded
// in storage.json. Every boot sets up the volumes storage.json records, so the running image
// mounts such a volume too although its config does not name it; its slot counts as removing the
// volume only once it is written again. It returns the warnings of the image's check. The caller
// holds s.mu.
func (s *Server) writeSlot(ctx context.Context, st imageStore, system, platform string, rootHash []byte, docs []config.Document, secret string) ([]string, error) {
	w, err := s.planSlot(ctx, st, system, platform, rootHash, docs, secret)
	if err != nil {
		return nil, err
	}
	if err := s.applyMachine(ctx, w.machine, secret); err != nil {
		return nil, err
	}
	if err := slot.Write(slot.Dir(s.Loader.Slots, rootHash), w.next); err != nil {
		return nil, failed(connect.CodeInternal, "store the image's config: %v", err)
	}
	log.Printf("the config %s of %x is stored", w.next.Version, rootHash)
	return w.warnings, nil
}

// plain is err without the code a connect error carries. An upgrade's error has a code of its own.
func plain(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

// imageHeader is the image an install or upgrade header describes.
func imageHeader(h *nodev1.ImageHeader) upgrade.Header {
	return upgrade.Header{
		ImageID: h.GetImageId(), Platform: h.GetPlatform(), Architecture: h.GetArchitecture(), RootHash: h.GetRootHash(),
		StoreSize: int64(h.GetStore().GetSize()), VeritySize: int64(h.GetHashTree().GetSize()), UKISize: int64(h.GetUki().GetSize()),
		StoreSHA256: h.GetStore().GetSha256(), VeritySHA256: h.GetHashTree().GetSha256(), UKISHA256: h.GetUki().GetSha256(),
		BootLoaderSize: int64(h.GetBootLoader().GetSize()), BootLoaderSHA256: h.GetBootLoader().GetSha256(),
		Provenance: upgrade.Provenance{Revision: h.GetRevision(), Tag: h.GetTag()},
	}
}

// recordImages forgets every image no UKI on the ESP boots any more, an entry's or the staged
// one's, in one step:
//   - the record of installed images keeps only the others;
//   - the config slots keep only those of the others and of the running image.
//
// installed, when set, is the image an upgrade installed or found installed; the record keeps its
// provenance. Failing to forget fails no upgrade, because both only shrink and a boot loads only
// its own image's slot. The next upgrade forgets again.
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

// installImage installs an upgrade's image on the node's boot disk, with the config text delivers
// for it and the fallback secret new encrypted volumes are enrolled with, and stages it there.
// Unless stageOnly is set it then activates the image, as ActivateImage does. Next to the result
// it returns the config stageConfig wrote to the image's slot; none when no slot was written, as
// for an image the node runs already or the known-good image kept with its config.
func (s *Server) installImage(ctx context.Context, h upgrade.Header, text, secret string, stageOnly bool, image io.Reader) (upgrade.Result, imageConfig, error) {
	if s.InstallImage != nil {
		return s.InstallImage(ctx, h, text, secret, stageOnly, image)
	}
	n, err := s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, imageConfig{}, err
	}
	var staged imageConfig
	n.CheckStore = func(st upgrade.Store) error {
		c, err := s.stageConfig(ctx, st, st.Toplevel, h.Platform, h.RootHash, text, secret)
		if err != nil {
			return err
		}
		staged = c
		return nil
	}
	install := n.Upgrade
	if stageOnly {
		install = n.Install
	}
	res, err := install(ctx, h, image)
	return res, staged, err
}

// ActivateImage makes the image staged in the inactive slot boot next, as upgrade.Node.Activate
// does, under the lock an upgrade and a rollback hold, so none of them writes the ESP while
// another does. It runs no check of the config: the image's own checker passed its slot before
// the image was staged. A staged image whose config slot STATE no longer keeps is refused before
// anything changes: it would boot without config, fail its health check and fall back. Refusals
// fail the precondition, and a failure to read or change the disk or the ESP is internal.
func (s *Server) ActivateImage(ctx context.Context, req *connect.Request[nodev1.ActivateImageRequest]) (*connect.Response[nodev1.ActivateImageResponse], error) {
	root := req.Msg.RootHash
	if len(root) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the request names no image to activate")
	}
	if !s.upgrading.TryLock() {
		return nil, failed(connect.CodeAborted, "an upgrade, an image rollback, a config delivery or the rollback at a trial's deadline is running")
	}
	defer s.upgrading.Unlock()
	if err := s.refuseOnTrialLocking("an image's activation"); err != nil {
		return nil, err
	}
	res, err := s.activateStaged(ctx, root)
	if err != nil {
		log.Printf("activation of %x failed: %v", root, err)
		code := connect.CodeInternal
		if refusal := (*upgrade.RefusalError)(nil); errors.As(err, &refusal) {
			code = connect.CodeFailedPrecondition
		}
		return nil, failed(code, "activate: %v", err)
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

// activateStaged activates the image staged in the inactive slot, by its root hash, on the boot
// disk.
func (s *Server) activateStaged(ctx context.Context, rootHash []byte) (upgrade.Result, error) {
	if s.ActivateStaged != nil {
		return s.ActivateStaged(ctx, rootHash)
	}
	n, err := s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, err
	}
	n.CheckStaged = s.checkStagedSlot
	return n.Activate(ctx, rootHash)
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

// upgradeNode is the node an upgrade changes. It reads and changes the boot disk's partition
// table under s.mu, which ApplyConfig and ResetVolume change the boot disk's partitions under.
func (s *Server) upgradeNode() (*upgrade.Node, error) {
	n, err := s.upgradeNodeLocked()
	if err != nil {
		return nil, err
	}
	n.TableLock = &s.mu
	return n, nil
}

// upgradeNodeLocked is upgradeNode for a caller that holds s.mu.
func (s *Server) upgradeNodeLocked() (*upgrade.Node, error) {
	boot, err := s.Host.ResolvePath(s.Paths.BootDisk)
	if err != nil {
		return nil, fmt.Errorf("find the disk the node booted from: %w", err)
	}
	return &upgrade.Node{
		Run:           s.Run,
		Disk:          boot.Device,
		ESP:           s.Paths.ESP,
		EFIVars:       s.Paths.EFIVars,
		Cmdline:       s.Paths.Cmdline,
		OSRelease:     readOSRelease(s.Paths.OSRelease),
		Architecture:  uki.GoArchitecture(runtime.GOARCH),
		OpenPartition: storeslot.OpenPartition,
	}, nil
}
