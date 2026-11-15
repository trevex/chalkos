package chalkd

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// stagedImage is the root hash of the image in the inactive slot that the node keeps a config slot
// for, staged or active (booting next), which everything that reads or writes its config treats
// alike; nil without one. A node with no config slot but the running image's has none, whatever
// the ESP holds.
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

// keepsKnownGood refuses config for the image of the root hash while it is the node's known-good
// image, which boots with the config it last ran. text, the config delivered, passes when it is
// that config; empty delivers none. It reports whether the image is the known-good one. The caller
// holds s.mu.
func (s *Server) keepsKnownGood(rootHash []byte, text string) (bool, error) {
	sl, ok, err := s.stagedEntry()
	if err != nil {
		return false, fmt.Errorf("find the staged image: %w", err)
	}
	if !ok || !bytes.Equal(sl.entry.RootHash, rootHash) || !sl.knownGood() {
		return false, nil
	}
	if text == "" {
		return true, nil
	}
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return true, layer.Errorf(connect.CodeInvalidArgument, "parse the config: %w", err)
	}
	version, err := config.Hash(docs)
	if err != nil {
		return true, layer.Errorf(connect.CodeInvalidArgument, "hash the config: %w", err)
	}
	held, err := slot.Version(slot.Dir(s.Loader.Slots, rootHash))
	if err != nil {
		return true, fmt.Errorf("read the staged image's config slot: %w", err)
	}
	if version != held {
		return true, layer.Errorf(connect.CodeFailedPrecondition, "the image %x staged to boot next is the one the node falls back to while the boot of %s is not found healthy; its config stays as it last ran until that boot is found healthy", rootHash, sl.booted.File)
	}
	return true, nil
}

// applyStaged writes the request's config to the slot of the image in the inactive slot, staged
// or active, once that image's checker passed it against the running config and the machine; the
// storage changes it makes use the request's fallback secret. dry_run answers with the plan and
// changes nothing. Nothing the running image runs changes. Without a staged image the config is
// held pending for the next upgrade; with one, a pending config stays as it is. The caller holds
// s.mu.
func (s *Server) applyStaged(ctx context.Context, m *nodev1.ApplyConfigRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if m.Config == "" || len(m.Layers) > 0 || len(m.NodeCertificate) > 0 || len(m.NodeKey) > 0 || len(m.Trust) > 0 {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "a request for the staged image delivers config alone, with the fallback secret: the share, the certificates and the OS CAs are the node's")
	}
	docs, err := config.Parse(strings.NewReader(m.Config))
	if err != nil {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "parse the config: %w", err)
	}
	staged, err := s.stagedImage()
	if err != nil {
		return nil, fmt.Errorf("find the staged image: %w", err)
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
			return nil, fmt.Errorf("read the staged image's config slot: %w", err)
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
		if err := s.imageSlots().Read(ctx, staged, func(st imageStore, system string) error {
			var err error
			w, err = s.planSlot(ctx, st, system, platform, staged, docs, m.FallbackSecret, nil)
			return err
		}); err != nil {
			return nil, err
		}
		resp := &nodev1.ApplyConfigResponse{Plan: &nodev1.Plan{Version: w.next.Version, Replaces: held}, Warnings: w.warnings}
		resp.Changes = append(resp.Changes, storageChanges(w.machine.changes)...)
		return connect.NewResponse(resp), nil
	}
	recorded, _, err := s.recorded()
	if err != nil {
		return nil, err
	}
	var warnings []string
	if err := s.imageSlots().Read(ctx, staged, func(st imageStore, system string) error {
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
	resp.Changes = append(resp.Changes, storageChanges(storage.Changes(recorded, after))...)
	return connect.NewResponse(resp), nil
}

// secondWrite is what config delivered to the running image writes besides the running slot: the
// slot of the image in the inactive slot, which that image checks first, as every slot is checked
// by the image that boots it, or else the pending config, unchecked until the next upgrade.
// Neither is set for a delivery without config or a node that has neither.
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

// planSecond plans the secondWrite of the delivery d before anything changes, so a refusal by the
// staged image refuses the delivery whole. A known-good image counts as none: it keeps the config
// it last ran, and the pending config is replaced instead. The staged image checks the config
// against the storage as d leaves it, since d's changes are made first and the staged image's on
// top of them, so each image's rules see the storage its change starts from. The caller holds s.mu
// and s.upgrading.
func (s *Server) planSecond(ctx context.Context, d *delivery) (secondWrite, error) {
	if d.keep {
		return secondWrite{}, nil
	}
	sl, ok, err := s.stagedEntry()
	if err != nil {
		return secondWrite{}, fmt.Errorf("find the staged image: %w", err)
	}
	if !ok || sl.knownGood() {
		var w secondWrite
		if ok {
			w.warnings = []string{fmt.Sprintf("the image %x staged to boot next is the one the node falls back to while the boot of %s is not found healthy; its config stays as it last ran", sl.entry.RootHash[:6], sl.booted.File)}
		}
		if w.pending, err = slot.Version(slot.Pending(s.Loader.Slots)); err != nil {
			return secondWrite{}, fmt.Errorf("read the pending config's version: %w", err)
		}
		if w.pending != "" {
			w.next = slot.Stored{Delivered: d.stored.Delivered, Version: d.stored.Version}
		}
		return w, nil
	}
	staged := sl.entry.RootHash
	held, err := slot.Version(slot.Dir(s.Loader.Slots, staged))
	if err != nil {
		return secondWrite{}, fmt.Errorf("read the staged image's config slot: %w", err)
	}
	recorded := d.machine.recorded
	if len(d.machine.changes) > 0 {
		recorded = d.machine.section
	}
	platform := readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]
	var w slotWrite
	if err := s.imageSlots().Read(ctx, staged, func(st imageStore, system string) error {
		var err error
		w, err = s.planSlot(ctx, st, system, platform, staged, d.stored.Delivered, d.msg.FallbackSecret, &recorded)
		return err
	}); err != nil {
		return secondWrite{}, s.stagedRefusal(staged, err, d.msg.FallbackSecret)
	}
	return secondWrite{image: staged, slot: w, held: held}, nil
}

// stagedRefusal is the refusal of a delivery for err, the staged image's refusal; secret is the
// request's fallback secret. chalkctl sends the secret only when the running image's storage
// enrolls it, so a staged image that adds an encrypted volume of its own is refused without it,
// and the refusal names config apply --staged with the secrets file. The staged image's checker
// is another chalkd whose errors arrive as text, so storage.NoFallbackSecret at the end of its
// rule's message tells this refusal apart.
func (s *Server) stagedRefusal(staged []byte, err error, secret string) error {
	var hint string
	if secret == "" && strings.Contains(err.Error(), storage.NoFallbackSecret) {
		hint = fmt.Sprintf("; the staged image's storage enrolls the fallback secret on a volume the running image's does not, and chalkctl sends the secret for the running image's storage alone: chalkctl config apply --staged %s --secrets FILE delivers the config to the staged image with the secret", s.nodeName())
	}
	return fmt.Errorf("the image %x staged in the inactive slot: %w%s", staged[:6], err, hint)
}

// write writes what planSecond planned once the running image applied version, and adds it to
// resp. A failed write leaves the slot or the pending config as it was and fails the delivery,
// naming what it did so far; the running image runs the config all the same, and the delivery
// sent again writes it. The caller holds s.mu.
//
// The staged image's storage changes go first, recorded in storage.json, and its slot after:
// every boot sets up the volumes storage.json records and no others, so the slot never names a
// volume that does not exist. At worst a recorded volume no slot names yet is left, which every
// boot mounts and a delivery sent again finds made.
func (w secondWrite) write(ctx context.Context, s *Server, version, secret string, resp *nodev1.ApplyConfigResponse) error {
	resp.Warnings = append(resp.Warnings, w.warnings...)
	switch {
	case w.image != nil:
		// The running image made its changes to the machine by now, which planSecond planned the
		// staged image's on top of.
		mc, err := s.machineChanges(w.slot.machine.section)
		if err == nil {
			err = s.applyMachine(ctx, mc, secret)
			made := mc.changes
			if err != nil {
				made = s.recordedSince(mc.recorded)
			}
			resp.Changes = append(resp.Changes, storageChanges(made)...)
		}
		if err == nil {
			if err = slot.Write(slot.Dir(s.Loader.Slots, w.image), w.slot.next); err != nil {
				err = fmt.Errorf("store the config: %w", err)
			}
		}
		if err != nil {
			return fmt.Errorf("the node runs config %s%s, but writing the config slot of the image %x staged in the inactive slot failed, so it keeps config %s: %w; deliver the config again to write it", version, didOf(resp), w.image[:6], cmp.Or(w.held, "none"), err)
		}
		log.Printf("the image %x staged in the inactive slot has config %s too", w.image[:6], w.slot.next.Version)
		resp.Warnings = append(resp.Warnings, w.stagedWarnings()...)
		resp.Staged = &nodev1.StagedSlot{Version: w.slot.next.Version, Replaces: w.held}
	case w.pending != "":
		p, err := s.storePending(w.next)
		if err != nil {
			return fmt.Errorf("the node runs config %s%s, but replacing the pending config %s with it failed, so it stays: %w; deliver the config again to replace it", version, didOf(resp), w.pending, err)
		}
		resp.Pending = p
	}
	return nil
}

// recordedSince is what storage.json records beyond recorded, from before a change that failed.
// A change records all its volumes at once after it made them, so a partition made before a
// failure that kept it from the record is not listed; the change sent again finds it made. A
// record that cannot be read lists none, which is logged.
func (s *Server) recordedSince(recorded storage.Section) []storage.Change {
	after, _, err := s.recorded()
	if err != nil {
		log.Printf("read what the failed storage change recorded: %v", err)
		return nil
	}
	return storage.Changes(recorded, after)
}

// didOf is what a delivery whose answer is resp did, for an error that loses the answer: its
// storage changes, the units it restarted and its warnings, in parentheses after a space; empty
// when it did none of them.
func didOf(resp *nodev1.ApplyConfigResponse) string {
	var did []string
	for _, c := range resp.Changes {
		did = append(did, fmt.Sprintf("volume %s: %s", c.Volume, c.Reason))
	}
	for _, u := range resp.RestartedUnits {
		did = append(did, "restarted "+u)
	}
	for _, w := range resp.Warnings {
		did = append(did, "warning: "+w)
	}
	if len(did) == 0 {
		return ""
	}
	return " (" + strings.Join(did, "; ") + ")"
}

// plan adds to resp, a dry run's answer, what write would write: the staged image's slot and the
// storage changes its config makes, or the pending config.
func (w secondWrite) plan(resp *nodev1.ApplyConfigResponse) {
	resp.Warnings = append(resp.Warnings, w.warnings...)
	switch {
	case w.image != nil:
		resp.Changes = append(resp.Changes, storageChanges(w.slot.machine.changes)...)
		resp.Warnings = append(resp.Warnings, w.stagedWarnings()...)
		resp.Staged = &nodev1.StagedSlot{Version: w.slot.next.Version, Replaces: w.held}
	case w.pending != "":
		resp.Pending = &nodev1.PendingConfig{Version: w.next.Version}
	}
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
