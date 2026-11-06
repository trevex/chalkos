package chalkd

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// stagedImage is the root hash of the image in the inactive slot that the node keeps a config slot
// for: staged, as an upgrade that stopped before the activation leaves it, or active, booting next
// instead of the running one, as an upgrade or a rollback without a reboot leaves it; nil without
// one. Everything that reads or writes the staged image's config treats both alike. A node whose
// config slots are the running image's alone has none, whatever the ESP holds: no upgrade wrote
// config for another.
func (s *Server) stagedImage() ([]byte, error) {
	sl, ok, err := s.stagedEntry()
	if err != nil || !ok {
		return nil, err
	}
	return sl.entry.RootHash, nil
}

// secondSlot is the image in the inactive slot, as stagedEntry finds it: its UKI, the entry that
// boots it next while it is active or the staged UKI while it is not, and the entry the node
// booted.
type secondSlot struct {
	entry, booted esp.Entry
	active        bool
}

// knownGood reports whether the image is the node's known-good image, as upgrade.KnownGood tells:
// an active one a rollback from a boot that was not found healthy staged. An image staged and not
// active never is.
func (sl secondSlot) knownGood() bool {
	return sl.active && upgrade.KnownGood(sl.entry, sl.booted)
}

// stagedEntry finds the image in the inactive slot, as stagedImage names it: the entry that boots
// next instead of the booted one, or else the UKI staged under its staged name; false without
// either.
func (s *Server) stagedEntry() (secondSlot, bool, error) {
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		return secondSlot{}, false, err
	}
	hashes, err := slot.List(s.Loader.Slots)
	if err != nil {
		return secondSlot{}, false, err
	}
	if !slices.ContainsFunc(hashes, func(h []byte) bool { return !bytes.Equal(h, running) }) {
		return secondSlot{}, false, nil
	}
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		return secondSlot{}, false, err
	}
	booted, err := upgrade.Booted(entries, s.Paths.EFIVars, running)
	if err != nil {
		return secondSlot{}, false, err
	}
	imageID := readOSRelease(s.Paths.OSRelease)["IMAGE_ID"]
	if e, ok := esp.StagedEntry(entries, imageID, booted); ok {
		return secondSlot{entry: e, booted: booted, active: true}, true, nil
	}
	e, ok, err := esp.Staged(s.Paths.ESP, imageID)
	if err != nil || !ok {
		return secondSlot{}, false, err
	}
	return secondSlot{entry: e, booted: booted}, true, nil
}

// keepsKnownGood refuses config for the image of the root hash while it is staged as the node's
// known-good image, as secondSlot.knownGood tells: a rollback from a boot that was not found
// healthy staged it, and it boots with the config it last ran. text, the config delivered, passes
// when it is that config; empty delivers none. It reports whether the image is the known-good one.
// The caller holds s.mu.
func (s *Server) keepsKnownGood(rootHash []byte, text string) (bool, error) {
	sl, ok, err := s.stagedEntry()
	if err != nil {
		return false, failed(connect.CodeInternal, "find the staged image: %v", err)
	}
	if !ok || !bytes.Equal(sl.entry.RootHash, rootHash) || !sl.knownGood() {
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
		return true, failed(connect.CodeFailedPrecondition, "the image %x staged to boot next is the one the node falls back to while the boot of %s is not found healthy; its config stays as it last ran until that boot is found healthy", rootHash, sl.booted.File)
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

// applyStaged checks the config of a request against the image in the inactive slot, staged or
// active, and keeps it in that image's slot, as an upgrade that finds the image staged already
// does: checked by that image's own checker against the running slot's config, and against the
// machine, whose storage changes the image accepts it makes with the request's fallback secret.
// With dry_run it checks the config the same way and answers with the plan, the slot's version
// before and after and the storage changes, and changes nothing. Nothing the running image runs
// changes. Without an image staged, the config is held pending for the next upgrade, as
// holdPending keeps it; a pending config stays as it is while an image is staged, as after a
// rollback that staged the image before. The caller holds s.mu.
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
		return s.holdPending(m, docs)
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
			w, err = s.planSlot(ctx, st, system, platform, staged, docs, m.FallbackSecret, nil)
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
	log.Printf("the image %x staged in the inactive slot has new config", staged[:6])
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

// secondWrite is what config delivered to the running image writes besides the running slot, as
// planSecond plans it before anything changes: the config slot of the image staged in the
// inactive slot, staged or active, or else the node's pending config. Every config slot is
// written only once the image that boots it checked it, so the staged image checks the config
// too; the pending config waits, unchecked as apply-config --staged holds it, for the image of
// the next upgrade, which then boots the config delivered last. Neither is set for a delivery
// without config, or for a node that has neither.
type secondWrite struct {
	// image is the root hash of the staged image, slot its slot as that image's checker passed it,
	// and held the version the slot holds until it is written.
	image []byte
	slot  slotWrite
	held  string
	// pending is the version of the pending config the delivery replaces with next, the config
	// as delivered.
	pending string
	next    slot.Stored
	// warnings say what the delivery leaves as it is: the config of the known-good image.
	warnings []string
}

// planSecond plans what the delivery d writes besides the running slot, as secondWrite says,
// before anything changes, so a refusal of the staged image refuses the delivery whole. The
// known-good image a rollback staged counts as no staged image: it keeps the config it last ran,
// as keepsKnownGood keeps it, and the pending config is replaced as on a node without a staged
// image. The caller holds s.mu and s.upgrading, as for every write of a slot.
//
// The staged image checks the config with its own checker as for apply-config --staged, and
// against the storage STATE records once the running image applied d: d's changes are made
// before the staged slot is written, and the changes the staged image's config makes on top of
// them are planned and made after. The rules of both images thus see the storage each change
// starts from.
func (s *Server) planSecond(ctx context.Context, d *delivery) (secondWrite, error) {
	if d.keep {
		return secondWrite{}, nil
	}
	sl, ok, err := s.stagedEntry()
	if err != nil {
		return secondWrite{}, failed(connect.CodeInternal, "find the staged image: %v", err)
	}
	if !ok || sl.knownGood() {
		var w secondWrite
		if ok {
			w.warnings = []string{fmt.Sprintf("the image %x staged to boot next is the one the node falls back to while the boot of %s is not found healthy; its config stays as it last ran", sl.entry.RootHash[:6], sl.booted.File)}
		}
		if w.pending, err = slot.Version(slot.Pending(s.Loader.Slots)); err != nil {
			return secondWrite{}, failed(connect.CodeInternal, "read the pending config's version: %v", err)
		}
		if w.pending != "" {
			w.next = slot.Stored{Delivered: d.stored.Delivered, Version: d.stored.Version}
		}
		return w, nil
	}
	staged := sl.entry.RootHash
	held, err := slot.Version(slot.Dir(s.Loader.Slots, staged))
	if err != nil {
		return secondWrite{}, failed(connect.CodeInternal, "read the staged image's config slot: %v", err)
	}
	recorded := d.machine.recorded
	if len(d.machine.changes) > 0 {
		recorded = d.machine.section
	}
	platform := readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]
	var w slotWrite
	if err := s.readImage(ctx, staged, func(st imageStore, system string) error {
		var err error
		w, err = s.planSlot(ctx, st, system, platform, staged, d.stored.Delivered, d.msg.FallbackSecret, &recorded)
		return err
	}); err != nil {
		return secondWrite{}, failed(codeOf(err), "the image %x staged in the inactive slot: %v", staged[:6], plain(err))
	}
	return secondWrite{image: staged, slot: w, held: held}, nil
}

// write writes what planSecond planned, once the running image applied version, the delivered
// config, and adds it to resp: the staged image's slot, after the storage changes its config
// makes on top of those of the running image, or the pending config. A write that fails leaves
// the slot or the pending config as it was and fails the delivery, naming it; the running image
// runs the config all the same, and the delivery sent again writes it. The caller holds s.mu.
func (w secondWrite) write(ctx context.Context, s *Server, version, secret string, resp *nodev1.ApplyConfigResponse) error {
	resp.Warnings = append(resp.Warnings, w.warnings...)
	switch {
	case w.image != nil:
		// The running image made its changes to the machine by now, which planSecond planned the
		// staged image's on top of.
		mc, err := s.machineChanges(w.slot.machine.section)
		if err == nil {
			err = s.applyMachine(ctx, mc, secret)
		}
		if err == nil {
			if err = slot.Write(slot.Dir(s.Loader.Slots, w.image), w.slot.next); err != nil {
				err = fmt.Errorf("store the config: %w", err)
			}
		}
		if err != nil {
			return failed(codeOf(err), "the node runs config %s, but writing the config slot of the image %x staged in the inactive slot failed, so it keeps config %s: %v; deliver the config again to write it", version, w.image[:6], cmp.Or(w.held, "none"), plain(err))
		}
		log.Printf("the image %x staged in the inactive slot has config %s too", w.image[:6], w.slot.next.Version)
		for _, c := range mc.changes {
			resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
		}
		resp.Warnings = append(resp.Warnings, w.stagedWarnings()...)
		resp.Staged = &nodev1.StagedSlot{Version: w.slot.next.Version, Replaces: w.held}
	case w.pending != "":
		p, err := s.storePending(w.next)
		if err != nil {
			return failed(connect.CodeInternal, "the node runs config %s, but replacing the pending config %s with it failed, so it stays: %v; deliver the config again to replace it", version, w.pending, err)
		}
		resp.Pending = p
	}
	return nil
}

// plan adds to resp, a dry run's answer, what write would write: the staged image's slot and the
// storage changes its config makes, or the pending config.
func (w secondWrite) plan(resp *nodev1.ApplyConfigResponse) {
	resp.Warnings = append(resp.Warnings, w.warnings...)
	switch {
	case w.image != nil:
		for _, c := range w.slot.machine.changes {
			resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
		}
		resp.Warnings = append(resp.Warnings, w.stagedWarnings()...)
		resp.Staged = &nodev1.StagedSlot{Version: w.slot.next.Version, Replaces: w.held}
	case w.pending != "":
		resp.Pending = &nodev1.PendingConfig{Version: w.next.Version}
	}
}

// codeOf is the code err carries, or internal for an error without one: a failure of the node
// itself.
func codeOf(err error) connect.Code {
	if ce := new(connect.Error); errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeInternal
}

// stagedWarnings are the warnings of the staged image's check, which name that image.
func (w secondWrite) stagedWarnings() []string {
	var out []string
	for _, warning := range w.slot.warnings {
		out = append(out, fmt.Sprintf("the image %x staged in the inactive slot: %s", w.image[:6], warning))
	}
	return out
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
