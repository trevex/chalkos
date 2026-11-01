package chalkd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"connectrpc.com/connect"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// stagedImage is the root hash of the image staged to boot next instead of the running one, as an
// upgrade or a rollback without a reboot leaves it; nil without one. A node whose config slots are
// the running image's alone has none, whatever the ESP holds: no upgrade wrote config for another.
func (s *Server) stagedImage() ([]byte, error) {
	staged, _, ok, err := s.stagedEntry()
	if err != nil || !ok {
		return nil, err
	}
	return staged.RootHash, nil
}

// stagedEntry is the entry of the image staged to boot next, as stagedImage finds it, with the
// entry the node booted; false without one.
func (s *Server) stagedEntry() (staged, booted esp.Entry, ok bool, err error) {
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		return esp.Entry{}, esp.Entry{}, false, err
	}
	hashes, err := slot.List(s.Loader.Slots)
	if err != nil {
		return esp.Entry{}, esp.Entry{}, false, err
	}
	if !slices.ContainsFunc(hashes, func(h []byte) bool { return !bytes.Equal(h, running) }) {
		return esp.Entry{}, esp.Entry{}, false, nil
	}
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		return esp.Entry{}, esp.Entry{}, false, err
	}
	booted, err = upgrade.Booted(entries, s.Paths.EFIVars, running)
	if err != nil {
		return esp.Entry{}, esp.Entry{}, false, err
	}
	staged, ok = esp.StagedEntry(entries, readOSRelease(s.Paths.OSRelease)["IMAGE_ID"], booted)
	return staged, booted, ok, nil
}

// keepsKnownGood refuses config for the image of the root hash while it is staged as the node's
// known-good image, as upgrade.KnownGood tells: a rollback from a boot that was not found healthy
// staged it, and it boots with the config it last ran. text, the config delivered, passes when it
// is that config; empty delivers none. It reports whether the image is the known-good one. The
// caller holds s.mu.
func (s *Server) keepsKnownGood(rootHash []byte, text string) (bool, error) {
	staged, booted, ok, err := s.stagedEntry()
	if err != nil {
		return false, failed(connect.CodeInternal, "find the staged image: %v", err)
	}
	if !ok || !bytes.Equal(staged.RootHash, rootHash) || !upgrade.KnownGood(staged, booted) {
		return false, nil
	}
	if text == "" {
		return true, nil
	}
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return true, failed(connect.CodeInvalidArgument, "parse the config: %v", err)
	}
	version, err := config.Hash(docs)
	if err != nil {
		return true, failed(connect.CodeInvalidArgument, "hash the config: %v", err)
	}
	held, err := slot.Version(slot.Dir(s.Loader.Slots, rootHash))
	if err != nil {
		return true, failed(connect.CodeInternal, "read the staged image's config slot: %v", err)
	}
	if version != held {
		return true, failed(connect.CodeFailedPrecondition, "the image %x staged to boot next is the one the node falls back to while the boot of %s is not found healthy; its config stays as it last ran until that boot is found healthy", rootHash, booted.File)
	}
	return true, nil
}

// readImage reads the store of an image the inactive slot holds, as an upgrade checks one, and
// passes it to read with the image's NixOS system in it. The caller holds s.mu, which the boot
// disk's partition table is read under.
func (s *Server) readImage(ctx context.Context, rootHash []byte, read func(st imageStore, system string) error) error {
	if s.ReadImage != nil {
		return s.ReadImage(ctx, rootHash, read)
	}
	n, err := s.upgradeNodeLocked()
	if err != nil {
		return err
	}
	return n.ReadImage(ctx, rootHash, func(st upgrade.Store) error { return read(st, st.Toplevel) })
}

// applyStaged checks the config of a request against the image staged to boot next and keeps it in
// that image's slot, as an upgrade that finds the image staged already does: checked by that
// image's own checker against the running slot's config, and against the machine, whose storage
// changes the image accepts it makes with the request's fallback secret. With dry_run it checks
// the config the same way and answers with the plan, the slot's version before and after and the
// storage changes, and changes nothing. Nothing the running image runs changes. The caller holds
// s.mu.
func (s *Server) applyStaged(ctx context.Context, m *nodev1.ApplyConfigRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if m.Config == "" || len(m.Layers) > 0 || len(m.NodeCertificate) > 0 || len(m.NodeKey) > 0 || len(m.Trust) > 0 {
		return nil, failed(connect.CodeInvalidArgument, "a request for the staged image delivers config alone, with the fallback secret: the share, the certificates and the OS CAs are the node's")
	}
	docs, err := config.Parse(strings.NewReader(m.Config))
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "parse the config: %v", err)
	}
	staged, err := s.stagedImage()
	if err != nil {
		return nil, failed(connect.CodeInternal, "find the staged image: %v", err)
	}
	if staged == nil {
		return nil, failed(connect.CodeFailedPrecondition, "no image is staged to boot next; apply the config to the running image")
	}
	knownGood, err := s.keepsKnownGood(staged, m.Config)
	if err != nil {
		return nil, err
	}
	// A dry run plans the version the slot would hold in place of the one it holds.
	var held string
	if m.DryRun {
		if held, err = slot.Version(slot.Dir(s.Loader.Slots, staged)); err != nil {
			return nil, failed(connect.CodeInternal, "read the staged image's config slot: %v", err)
		}
	}
	// The known-good image's own config, delivered again, changes nothing.
	if knownGood {
		resp := &nodev1.ApplyConfigResponse{}
		if m.DryRun {
			resp.Plan = &nodev1.Plan{Version: held, Replaces: held}
		}
		return connect.NewResponse(resp), nil
	}
	platform := readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]
	// A dry run has the image check the config, as a delivery does, and stops before the machine
	// or the slot changes.
	if m.DryRun {
		var w slotWrite
		if err := s.readImage(ctx, staged, func(st imageStore, system string) error {
			var err error
			w, err = s.planSlot(ctx, st, system, platform, staged, docs, m.FallbackSecret)
			return err
		}); err != nil {
			return nil, err
		}
		resp := &nodev1.ApplyConfigResponse{Plan: &nodev1.Plan{Version: w.next.Version, Replaces: held}, Warnings: w.warnings}
		for _, c := range w.machine.changes {
			resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
		}
		return connect.NewResponse(resp), nil
	}
	recorded, _, err := s.recorded()
	if err != nil {
		return nil, err
	}
	var warnings []string
	if err := s.readImage(ctx, staged, func(st imageStore, system string) error {
		var err error
		warnings, err = s.writeSlot(ctx, st, system, platform, staged, docs, m.FallbackSecret)
		return err
	}); err != nil {
		return nil, err
	}
	log.Printf("the image %x staged to boot next has new config", staged[:6])
	after, _, err := s.recorded()
	if err != nil {
		return nil, err
	}
	resp := &nodev1.ApplyConfigResponse{Warnings: warnings}
	for _, c := range storage.Changes(recorded, after) {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return connect.NewResponse(resp), nil
}

// checkStaged refuses a change of the machine's storage to section when the config slot of the
// image staged to boot next could not be written again after it. That happens when the slot's
// config fails the storage rules against the new storage, for example when it lacks a volume the
// change adds.
//
// The staged image would still boot and mount such a volume, because every boot sets up the
// volumes storage.json records. Its next config, however, would be refused for removing it. The
// operator first brings the staged slot along with chalkctl apply-config --staged, after which
// the new volume is additive.
//
// The staged image checks its slot, as it would check it written again once the change is made:
//   - against its own storage rules, which see the node called name;
//   - with section, the storage this change makes, as the recorded storage, and next, the config
//     this change runs, as the running config;
//   - with secret, the delivery's fallback secret, for the facts that declare it.
//
// A write of the staged slot does not check the running slot in turn. The caller holds s.mu.
func (s *Server) checkStaged(ctx context.Context, name string, next []config.Document, section storage.Section, secret string) error {
	staged, err := s.stagedImage()
	if err != nil {
		return failed(connect.CodeInternal, "find the staged image: %v", err)
	}
	if staged == nil {
		return nil
	}
	held, found, err := slot.Read(slot.Dir(s.Loader.Slots, staged))
	if err != nil {
		return failed(connect.CodeInternal, "read the staged image's config slot: %v", err)
	}
	if !found {
		return nil
	}
	platform := readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]
	var refusal *checkv1.Refusal
	if err := s.readImage(ctx, staged, func(st imageStore, system string) error {
		res, err := s.runImageCheck(ctx, st, system, platform, name, imageCheck{delivered: held.Delivered, old: next, secret: secret, recorded: &section, storage: true, image: staged})
		refusal = res.GetRefusal()
		return err
	}); err != nil {
		code := connect.CodeInternal
		if ce := new(connect.Error); errors.As(err, &ce) {
			code = ce.Code()
		}
		return failed(code, "check the config of the image %x staged to boot next: %v", staged[:6], plain(err))
	}
	if refusal == nil {
		return nil
	}
	saying := refusal.GetMessage()
	if len(refusal.GetFailures()) > 0 {
		saying = strings.Join(refusal.GetFailures(), "; ")
	}
	return failed(connect.CodeFailedPrecondition, "the image %x staged to boot next would refuse its own config once this config changes the storage, saying: %s; bring its config along first: run chalkctl apply-config --staged %s with the new config, then apply it to the running image", staged[:6], saying, name)
}

// stagedVersion is the version of the config slot of the image the boot status names as staged;
// empty without one.
func (s *Server) stagedVersion(boot *nodev1.BootStatus) (string, error) {
	if boot.GetStaged() == nil {
		return "", nil
	}
	v, err := slot.Version(slot.Dir(s.Loader.Slots, boot.GetStaged().GetRootHash()))
	if err != nil {
		return "", fmt.Errorf("read the staged config's version: %w", err)
	}
	return v, nil
}
