package chalkd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki"
)

// machineChange is what a config slot's storage section adds to the machine's: storage.json, the
// storage as applied, and the changes the section makes to it.
type machineChange struct {
	recorded, section storage.Section
	changes           []storage.Change
}

// machineChanges compares a slot's storage section with the machine's, for every slot write of any
// image: the machine has one storage, which a slot's config may grow but not undo. A slot that
// lacks a volume another image's config added is compared only when written again; until then
// its image mounts the volume too, as every boot sets up what storage.json records and none
// removes a volume. The caller holds s.mu.
func (s *Server) machineChanges(section storage.Section) (machineChange, error) {
	recorded, _, err := s.recorded()
	if err != nil {
		return machineChange{}, err
	}
	return machineChange{recorded: recorded, section: section, changes: storage.Changes(recorded, section)}, nil
}

// applyMachine makes the additive changes a slot's section makes to the machine's storage, which
// records them in storage.json; secret is the fallback secret new encrypted volumes are enrolled
// with, which the rules checked. The caller holds s.mu.
func (s *Server) applyMachine(ctx context.Context, mc machineChange, secret string) error {
	if len(mc.changes) == 0 {
		return nil
	}
	return s.applyStorage(ctx, mc.recorded, mc.section, secret, mc.changes)
}

// applyStorage applies additive changes, as node.Storage.Change describes, once it checked that
// the fallback secret new encrypted volumes are enrolled with was sent.
func (s *Server) applyStorage(ctx context.Context, recorded, section storage.Section, secret string, changes []storage.Change) error {
	if err := checkFallback(recorded, section, secret); err != nil {
		return err
	}
	ctx, cancel := storageContext(ctx)
	defer cancel()
	return storageFailed(s.storage().Change(ctx, recorded, section, secret, changes))
}

// storageTimeout bounds a change of the node's storage once its request no longer does.
const storageTimeout = 30 * time.Minute

// storageContext detaches ctx for steps that change disks: a client that goes away must not kill
// wipefs, sfdisk, mkfs or systemd-cryptenroll halfway, which could leave a partition table half
// written or a volume wiped and still pinned.
func storageContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storageTimeout)
}

// storageChanges are the changes as the API answers them.
func storageChanges(changes []storage.Change) []*nodev1.StorageChange {
	var out []*nodev1.StorageChange
	for _, c := range changes {
		out = append(out, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return out
}

// storageFailed gives an error of a change to the node's storage its code: a failure of the node
// itself is internal, and anything else refuses the request.
func storageFailed(err error) error {
	switch {
	case err == nil:
		return nil
	case node.IsInternal(err):
		return err
	default:
		return layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
	}
}

// checkFallback refuses before anything changes when a volume the section adds to recorded is
// encrypted and the fallback secret it would be enrolled with is missing. Its refusal ends with
// storage.NoFallbackSecret, which chalkd matches in an upgrade's refusal.
func checkFallback(recorded, section storage.Section, secret string) error {
	if secret != "" {
		return nil
	}
	if name := enrolled(recorded, section); name != "" {
		return layer.Errorf(connect.CodeInvalidArgument, "volume %s is encrypted and the node's fallback is %s, but %s", name, section.Fallback, storage.NoFallbackSecret)
	}
	return nil
}

// enrolled returns the first volume the section adds to recorded that gets the fallback
// keyslot, or "" when there is none.
func enrolled(recorded, section storage.Section) string {
	if section.Fallback == storage.FallbackNone {
		return ""
	}
	for _, name := range slices.Sorted(maps.Keys(section.Volumes)) {
		if _, ok := recorded.Volumes[name]; !ok && section.Volumes[name].Encryption == storage.EncryptionTPM2 {
			return name
		}
	}
	return ""
}

func (s *Server) ResetVolume(ctx context.Context, m *nodev1.ResetVolumeRequest) (*nodev1.ResetVolumeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A reset writes storage.json and the pins, which an upgrade reads, and a trial's rollback
	// restores the node as it was before the trial, which a reset would change under it.
	if !s.upgrading.TryLock() {
		return nil, layer.Errorf(connect.CodeAborted, "an upgrade, an image rollback or the rollback at a trial's deadline is running; reset the volume once it ended")
	}
	defer s.upgrading.Unlock()
	if err := s.refuseOnTrial("a volume reset", true); err != nil {
		return nil, err
	}
	if m.Config == "" {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the request delivers no config, which defines the volume")
	}
	name, err := s.name()
	if err != nil {
		return nil, err
	}
	t, schemas, err := s.runningTarget()
	if err != nil {
		return nil, err
	}
	c, err := configure(name, m.Config, schemas, t)
	if err != nil {
		return nil, err
	}
	if err := s.resetVolume(ctx, name, m.Volume, c, m.FallbackSecret); err != nil {
		return nil, err
	}
	return &nodev1.ResetVolumeResponse{}, nil
}

// resetVolume wipes the volume name and creates it again as next, the config translated for the
// node called node on the running image, defines it; the caller holds s.mu. The image's storage
// rules decide what a reset carries out: once deleted, the volume is created again like a new one,
// so they are checked against the recorded section without it, which they must then accept
// whole. They check the disk the volume lives on and the fallback secret it is enrolled with too.
func (s *Server) resetVolume(ctx context.Context, node, name string, next slot.Stored, fallback string) error {
	d, err := s.parseIdentity(string(next.Identity))
	if err != nil {
		return err
	}
	recorded, pins, err := s.recorded()
	if err != nil {
		return err
	}
	v, ok := recorded.Volumes[name]
	if !ok {
		return layer.Errorf(connect.CodeNotFound, "the node has no volume %s", name)
	}
	if name == storage.VarVolume {
		return layer.Errorf(connect.CodeFailedPrecondition, "VAR holds the running node's data and cannot be reset while it runs")
	}
	nv, ok := d.section.Volumes[name]
	if !ok {
		return layer.Errorf(connect.CodeFailedPrecondition, "the config no longer has volume %s; a reset recreates a volume, it does not remove one", name)
	}
	if nv.Disk != v.Disk {
		return layer.Errorf(connect.CodeFailedPrecondition, "volume %s moves from disk %s to %s; a reset recreates a volume on its own disk only", name, v.Disk, nv.Disk)
	}
	without := recorded
	without.Volumes = maps.Clone(recorded.Volumes)
	delete(without.Volumes, name)
	set, err := s.Loader.Contract.Rules()
	if err != nil {
		return fmt.Errorf("the running image's rules: %w", err)
	}
	_, running, _, err := s.runningSlot()
	if err != nil {
		return err
	}
	nextDocs, runningDocs := config.Redact(next.Documents), config.Redact(running.Documents)
	f := s.factsFor(ctx, nextDocs, runningDocs, fallback)
	f.Override("recorded", jsonValue(without))
	storageRules := check.Image{ChangeRules: set.Of(config.StorageAPI)}
	if err := storageRules.Rules(rules.Input{Documents: nextDocs, Old: runningDocs, Node: node, Facts: f}); err != nil {
		if r, ok := errors.AsType[*check.Refusal](err); ok && len(r.Failures) > 0 {
			return fmt.Errorf("a reset of volume %s does not carry out the config: %w", name, err)
		}
		return err
	}
	// A reset recreates its volume alone: a change to another volume goes with a delivery, which
	// writes the config slot.
	changes := storage.Changes(without, d.section)
	for _, c := range changes {
		if c.Volume != name {
			return layer.Errorf(connect.CodeFailedPrecondition, "the config also changes volume %s (%s), which a reset of volume %s does not carry out; deliver the config first", c.Volume, c.Reason, name)
		}
	}
	if err := checkFallback(without, d.section, fallback); err != nil {
		return err
	}

	ctx, cancel := storageContext(ctx)
	defer cancel()
	if err := storageFailed(s.storage().DeleteVolume(ctx, name, v, pins)); err != nil {
		return err
	}
	if err := s.applyStorage(ctx, without, d.section, fallback, changes); err != nil {
		return err
	}
	log.Printf("volume %s reset", name)
	return nil
}

// elsewhere adds to err, a refusal of config whose storage section is section, the images whose
// config slots name a volume that section leaves out. It names them only for a volume the machine
// records and the running image's config never named: the config of one of those images added
// it, with an upgrade or config apply --staged, and naming the volume keeps it. The slot being
// written, writing, is left out: its config is the one being replaced. The caller holds s.mu.
func (s *Server) elsewhere(err error, section storage.Section, writing []byte) error {
	recorded, _, rerr := s.recorded()
	if rerr != nil {
		return err
	}
	_, running, found, rerr := s.runningSlot()
	if rerr != nil || !found {
		return err
	}
	ran, rerr := s.parseIdentity(string(running.Identity))
	if rerr != nil {
		return err
	}
	var notes []string
	for _, name := range slices.Sorted(maps.Keys(recorded.Volumes)) {
		if name == storage.VarVolume || hasVolume(section, name) || hasVolume(ran.section, name) {
			continue
		}
		if others := s.slotsNaming(name, writing); len(others) > 0 {
			notes = append(notes, fmt.Sprintf("the config of the image %s names volume %s", strings.Join(others, " and "), name))
		}
	}
	if len(notes) == 0 {
		return err
	}
	return fmt.Errorf("%w; %s", err, strings.Join(notes, "; "))
}

// slotsNaming lists, by the start of their root hashes, the images other than the running one and
// writing whose config slots name the volume.
func (s *Server) slotsNaming(name string, writing []byte) []string {
	runningHash, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		return nil
	}
	hashes, err := slot.List(s.Loader.Slots)
	if err != nil {
		return nil
	}
	var others []string
	for _, h := range hashes {
		if bytes.Equal(h, runningHash) || bytes.Equal(h, writing) {
			continue
		}
		held, found, err := slot.Read(slot.Dir(s.Loader.Slots, h))
		if err != nil || !found {
			continue
		}
		if d, err := s.parseIdentity(string(held.Identity)); err == nil && hasVolume(d.section, name) {
			others = append(others, fmt.Sprintf("%x", h[:6]))
		}
	}
	return others
}

func hasVolume(section storage.Section, name string) bool {
	_, ok := section.Volumes[name]
	return ok
}
