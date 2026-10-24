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

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// machineChange is what a config slot's storage section adds to the machine's: storage.json, the
// storage as applied, and the changes the section makes to it.
type machineChange struct {
	recorded, section storage.Section
	changes           []storage.Change
}

// machineChanges compares a slot's storage section with the machine's. Every write of a slot
// does, whichever image the slot is for: the machine has one storage, which a slot's config may
// grow but not undo. The image's rules refused the changes a node does not make already.
//
// A slot whose section lacks a volume the machine has, as one written before an upgrade or
// apply-config --staged added the volume, is compared only when it is written again. Until then
// its image mounts the volume too: every boot sets up each volume storage.json records, and
// nothing at boot removes one. The caller holds s.mu.
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
	return storageFailed(s.storage().Change(ctx, recorded, section, secret, changes))
}

// storageFailed gives an error of a change to the node's storage its code: a failure of the node
// itself is internal, and anything else refuses the request.
func storageFailed(err error) error {
	switch {
	case err == nil:
		return nil
	case node.IsInternal(err):
		return failed(connect.CodeInternal, "%v", err)
	default:
		return failed(connect.CodeFailedPrecondition, "%v", err)
	}
}

// checkFallback refuses before anything changes when a volume the section adds to recorded is
// encrypted and the fallback secret it would be enrolled with is missing.
func checkFallback(recorded, section storage.Section, secret string) error {
	if secret != "" {
		return nil
	}
	if name := enrolled(recorded, section); name != "" {
		return failed(connect.CodeInvalidArgument, "volume %s is encrypted and the node's fallback is %s, but no fallback secret was sent", name, section.Fallback)
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

func (s *Server) ResetVolume(ctx context.Context, req *connect.Request[nodev1.ResetVolumeRequest]) (*connect.Response[nodev1.ResetVolumeResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := req.Msg
	if m.Config == "" {
		return nil, failed(connect.CodeInvalidArgument, "the request delivers no config, which defines the volume")
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
	if err := s.resetVolume(ctx, name, m.Volume, c, schemas, m.FallbackSecret); err != nil {
		return nil, err
	}
	return connect.NewResponse(&nodev1.ResetVolumeResponse{}), nil
}

// resetVolume wipes the volume name and creates it again as next, the config translated for the
// node called node on the running image, whose schemas are given, defines it; the caller holds
// s.mu. The image's storage rules decide what a reset carries out: once deleted, the volume is
// created again like a new one, so they are checked against the recorded section without it, which
// they must then accept whole. They check the disk the volume lives on and the fallback secret it
// is enrolled with too.
func (s *Server) resetVolume(ctx context.Context, node, name string, next slot.Stored, schemas *schema.Schemas, fallback string) error {
	d, err := parseIdentity(string(next.Identity))
	if err != nil {
		return err
	}
	recorded, pins, err := s.recorded()
	if err != nil {
		return err
	}
	v, ok := recorded.Volumes[name]
	if !ok {
		return failed(connect.CodeNotFound, "the node has no volume %s", name)
	}
	if name == storage.VarVolume {
		return failed(connect.CodeFailedPrecondition, "VAR holds the running node's data and cannot be reset while it runs")
	}
	nv, ok := d.section.Volumes[name]
	if !ok {
		return failed(connect.CodeFailedPrecondition, "the config no longer has volume %s; a reset recreates a volume, it does not remove one", name)
	}
	if nv.Disk != v.Disk {
		return failed(connect.CodeFailedPrecondition, "volume %s moves from disk %s to %s; a reset recreates a volume on its own disk only", name, v.Disk, nv.Disk)
	}
	without := recorded
	without.Volumes = map[string]storage.Volume{}
	for n, vol := range recorded.Volumes {
		if n != name {
			without.Volumes[n] = vol
		}
	}
	set, err := compileRunningRules(schemas)
	if err != nil {
		return err
	}
	_, running, _, err := s.runningSlot()
	if err != nil {
		return err
	}
	f, err := s.factsFor(ctx, next.Documents, running.Documents, fallback)
	if err != nil {
		return err
	}
	f.Override("recorded", jsonValue(without))
	failures, err := evaluate(set.Of(config.StorageAPI), node, next.Documents, running.Documents, f)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return failed(connect.CodeFailedPrecondition, "a reset of volume %s does not carry out the config: %s", name, rules.Refusal(failures, node))
	}

	if err := storageFailed(s.storage().DeleteVolume(ctx, name, v, pins)); err != nil {
		return err
	}
	if err := s.applyStorage(ctx, without, d.section, fallback, storage.Changes(without, d.section)); err != nil {
		return err
	}
	log.Printf("volume %s reset", name)
	return nil
}

// elsewhere adds to err, a refusal of config whose storage section is section, the images whose
// config slots name a volume that section leaves out. It names them only for a volume the machine
// records and the running image's config never named: the config of one of those images added
// it, with an upgrade or apply-config --staged, and naming the volume keeps it. The slot being
// written, writing, is left out: its config is the one being replaced. The caller holds s.mu.
func (s *Server) elsewhere(err error, section storage.Section, writing []byte) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	recorded, _, rerr := s.recorded()
	if rerr != nil {
		return err
	}
	_, running, found, rerr := s.runningSlot()
	if rerr != nil || !found {
		return err
	}
	ran, rerr := parseIdentity(string(running.Identity))
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
	return failed(ce.Code(), "%s; %s", ce.Message(), strings.Join(notes, "; "))
}

// slotsNaming lists, by the start of their root hashes, the images other than the running one and
// writing whose config slots name the volume.
func (s *Server) slotsNaming(name string, writing []byte) []string {
	runningHash, err := s.runningHash()
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
		if d, err := parseIdentity(string(held.Identity)); err == nil && hasVolume(d.section, name) {
			others = append(others, fmt.Sprintf("%x", h[:6]))
		}
	}
	return others
}

func hasVolume(section storage.Section, name string) bool {
	_, ok := section.Volumes[name]
	return ok
}
