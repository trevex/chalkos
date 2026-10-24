package chalkd

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// stagedImage is the root hash of the image staged to boot next instead of the running one, as an
// upgrade without a reboot leaves it; nil without one. A node whose config slots are the running
// image's alone has none, whatever the ESP holds: no upgrade wrote config for another.
func (s *Server) stagedImage() ([]byte, error) {
	running, err := s.runningHash()
	if err != nil {
		return nil, err
	}
	hashes, err := slot.List(s.Loader.Slots)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(hashes, func(h []byte) bool { return !bytes.Equal(h, running) }) {
		return nil, nil
	}
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		return nil, err
	}
	booted, err := upgrade.Booted(entries, s.Paths.EFIVars, running)
	if err != nil {
		return nil, err
	}
	e, ok := esp.StagedEntry(entries, readOSRelease(s.Paths.OSRelease)["IMAGE_ID"], booted)
	if !ok {
		return nil, nil
	}
	return e.RootHash, nil
}

// readImage reads the store of an image the inactive slot holds, as an upgrade checks one, and
// passes it to read with the image's NixOS system in it. The caller holds s.mu, which the boot
// disk's partition table is read under.
func (s *Server) readImage(ctx context.Context, rootHash []byte, read func(st imageStore, system string) error) error {
	if s.ReadImage != nil {
		return s.ReadImage(ctx, rootHash, read)
	}
	n, err := s.upgradeNode(nil)
	if err != nil {
		return err
	}
	return n.ReadImage(ctx, rootHash, func(st upgrade.Store) error { return read(st, st.Toplevel) })
}

// applyStaged checks the config of a request against the image staged to boot next and keeps it in
// that image's slot, as an upgrade that finds the image staged already does: checked by that
// image's rules against the running slot's config and the machine, whose storage changes the rules
// accept it makes with the request's fallback secret. Nothing the running image runs changes.
// The caller holds s.mu.
func (s *Server) applyStaged(ctx context.Context, m *nodev1.ApplyConfigRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if m.Config == "" || len(m.KubernetesShare) > 0 || len(m.NodeCertificate) > 0 || len(m.NodeKey) > 0 || len(m.Trust) > 0 {
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
	recorded, _, err := s.recorded()
	if err != nil {
		return nil, err
	}
	platform := readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]
	if err := s.readImage(ctx, staged, func(st imageStore, system string) error {
		return s.writeSlot(ctx, st, system, platform, staged, docs, m.FallbackSecret)
	}); err != nil {
		return nil, err
	}
	log.Printf("the image %x staged to boot next has new config", staged[:6])
	after, _, err := s.recorded()
	if err != nil {
		return nil, err
	}
	resp := &nodev1.ApplyConfigResponse{}
	for _, c := range storage.Changes(recorded, after) {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return connect.NewResponse(resp), nil
}

// checkStaged refuses a change of the machine's storage to section that the config slot of the
// image staged to boot next could not be written again with: that slot's config fails the storage
// rules against the storage the change makes, as when it lacks a volume the change adds. That image
// would still boot and mount the volume, as every boot sets up the volumes storage.json records,
// but its next config would be refused for removing it. The staged slot is brought along first,
// with chalkctl apply-config --staged, to which the new volume is then additive. The slot is
// checked as it would be written again once the change is made: against section as the recorded
// storage and next, the config this change runs, as the running config, with the running image's
// storage rules, whose schemas are given, which see the node called name, and the delivery's facts
// f, which its commands read for the staged slot. The check runs this way only: a write of the
// staged slot does not check the running slot against the storage it makes. The caller holds s.mu.
func (s *Server) checkStaged(schemas *schema.Schemas, name string, next []config.Document, section storage.Section, f *facts.Loader) error {
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
	set, err := compileRules(schemas, true)
	if err != nil {
		return err
	}
	sf := f.For(factInput(held.Documents, next))
	sf.Override("recorded", jsonValue(section))
	failures, err := evaluate(set.Of(config.StorageAPI), name, held.Documents, next, sf)
	if err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	msgs := make([]string, len(failures))
	for i, f := range failures {
		msgs[i] = f.String()
	}
	return failed(connect.CodeFailedPrecondition, "the image %x staged to boot next would refuse its own config once this config changes the storage, saying: %s; bring its config along first: run chalkctl apply-config --staged %s with the new config, then apply it to the running image", staged[:6], strings.Join(msgs, "; "), name)
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
