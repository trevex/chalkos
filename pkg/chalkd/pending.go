package chalkd

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/storage"
)

// holdPending keeps docs, the config of a request with staged on a node without a staged image,
// as the node's pending config, in place of any it held, and answers with it. No image is there
// to check it against, so chalkd only parses it: the next upgrade checks it with the image it
// installs and writes it to that image's slot. A dry run keeps nothing. The fallback secret is
// refused rather than stored: a pending config waits on STATE for as long as no upgrade comes,
// and the upgrade that adds an encrypted volume carries the secret itself. The caller holds s.mu
// and s.upgrading, so no upgrade reads the pending config while it is replaced.
func (s *Server) holdPending(m *nodev1.ApplyConfigRequest, docs []config.Document) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if m.FallbackSecret != "" {
		return nil, failed(connect.CodeInvalidArgument, "no image is staged to boot next, so the config is held pending for the next upgrade, which is stored without the fallback secret; the upgrade that adds an encrypted volume carries it")
	}
	if len(docs) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the config holds no documents")
	}
	version, err := config.Hash(docs)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "hash the config: %v", err)
	}
	if m.DryRun {
		return connect.NewResponse(&nodev1.ApplyConfigResponse{Pending: &nodev1.PendingConfig{Version: version}}), nil
	}
	if err := slot.Write(slot.Pending(s.Loader.Slots), slot.Stored{Delivered: docs, Version: version}); err != nil {
		return nil, failed(connect.CodeInternal, "store the pending config: %v", err)
	}
	log.Printf("config %s is held pending for the next upgrade, unchecked", version)
	p, err := s.pendingConfig()
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	return connect.NewResponse(&nodev1.ApplyConfigResponse{Pending: p}), nil
}

// discardPending drops the node's pending config and answers with the one it dropped; a node
// without one answers with none. The caller holds s.mu and s.upgrading.
func (s *Server) discardPending(m *nodev1.ApplyConfigRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if !m.Staged || m.Config != "" || m.DryRun || m.FallbackSecret != "" || len(m.Layers) > 0 || len(m.NodeCertificate) > 0 || len(m.NodeKey) > 0 || len(m.Trust) > 0 || m.Try != nil || len(m.IgnoreUnits) > 0 {
		return nil, failed(connect.CodeInvalidArgument, "discard_pending goes with staged alone")
	}
	p, err := s.pendingConfig()
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if err := slot.Remove(slot.Pending(s.Loader.Slots)); err != nil {
		return nil, failed(connect.CodeInternal, "remove the pending config: %v", err)
	}
	if p != nil {
		log.Printf("the pending config %s is dropped", p.Version)
	}
	return connect.NewResponse(&nodev1.ApplyConfigResponse{Pending: p}), nil
}

// pendingConfig is the node's pending config, its version and when it was stored; nil without
// one.
func (s *Server) pendingConfig() (*nodev1.PendingConfig, error) {
	dir := slot.Pending(s.Loader.Slots)
	version, err := slot.Version(dir)
	if err != nil {
		return nil, fmt.Errorf("read the pending config's version: %w", err)
	}
	if version == "" {
		return nil, nil
	}
	// Status reads without s.mu, so an upgrade or a discard may remove the pending config between
	// the two reads; it is gone then, rather than stored at the zero time.
	since, ok, err := slot.Written(dir)
	if err != nil {
		return nil, fmt.Errorf("read when the pending config was stored: %w", err)
	}
	if !ok {
		return nil, nil
	}
	return &nodev1.PendingConfig{Version: version, Since: timestamppb.New(since)}, nil
}

// configSource is where the config an upgrade gives its image comes from.
type configSource int

const (
	// fromUpgrade is config the upgrade carries.
	fromUpgrade configSource = iota
	// fromPending is the node's pending config.
	fromPending
	// fromRunning is the running slot's config, carried over.
	fromRunning
)

// imageConfig is the config an upgrade gives the image it installs.
type imageConfig struct {
	docs []config.Document
	from configSource
	// pending is the version of the node's pending config, used or not; empty without one.
	pending string
}

// upgradeConfig is the config an upgrade gives the image it installs, the first there is of:
// text, the config the upgrade carries; the node's pending config; the running slot's documents
// as they were delivered. The caller holds s.mu.
func (s *Server) upgradeConfig(text string) (imageConfig, error) {
	stored, held, err := slot.Read(slot.Pending(s.Loader.Slots))
	if err != nil {
		return imageConfig{}, fmt.Errorf("read the pending config: %w", err)
	}
	c := imageConfig{pending: stored.Version}
	switch {
	case text != "":
		if c.docs, err = config.Parse(strings.NewReader(text)); err != nil {
			return imageConfig{}, fmt.Errorf("parse the config: %w", err)
		}
		return c, nil
	case held:
		c.docs, c.from = stored.Delivered, fromPending
		return c, nil
	}
	_, running, found, err := s.runningSlot()
	if err != nil {
		return imageConfig{}, plain(err)
	}
	if !found || len(running.Delivered) == 0 {
		return imageConfig{}, errors.New("the node holds no config to carry over to the image; upgrade with the config from an inventory, which needs the admin role")
	}
	c.docs, c.from = running.Delivered, fromRunning
	return c, nil
}

// refused is the error of an upgrade whose image refused the config c, which err says why,
// naming where the config came from and what to do about it. name is the node's name.
func (c imageConfig) refused(err error, name string) error {
	switch c.from {
	case fromUpgrade:
		return plain(err)
	case fromRunning:
		return fmt.Errorf("the node's config does not suit the image: %w; upgrade with config the image accepts, from an inventory, which needs the admin role", plain(err))
	}
	// A pending config is kept without the fallback secret, so only an upgrade that carries config
	// of its own can carry the secret too.
	if strings.Contains(err.Error(), storage.NoFallbackSecret) {
		return fmt.Errorf("the pending config %s does not suit the image: %w; a pending config is kept without the fallback secret, so upgrade with the config from an inventory, which carries the secret and needs the admin role", c.pending, plain(err))
	}
	return fmt.Errorf("the pending config %s does not suit the image: %w; chalkctl apply-config --staged %s replaces it, and chalkctl apply-config --staged --discard %s drops it", c.pending, plain(err), name, name)
}

// settlePending removes the pending config that c, the config stageConfig wrote to the image's
// slot, names: the one the image an upgrade staged took, or the one the config the upgrade carried
// replaced; c names none when no slot was written. That image boots next with its config now, and
// a later upgrade must not take the pending config. Only now, once the image is staged, may it go:
// an upgrade that fails after the image's slot was written, as when its UKI fails to activate,
// keeps it for the upgrade after. The image is staged already, so a failure to remove it does not
// fail the upgrade: the journal says so, Status keeps naming the pending config, and apply-config
// --staged --discard drops it. It returns what became of the pending config for the upgrade's
// answer, as chalkd knows it, since chalkd reboots soon after; nil when the node held none, or
// when reading one it keeps failed, which the journal records. The caller holds s.upgrading.
func (s *Server) settlePending(c imageConfig, rootHash []byte) *nodev1.PendingOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.pending == "" {
		// No slot was written, as for an image the node runs already or the known-good image, so a
		// pending config the node holds stays.
		p, err := s.pendingConfig()
		if err != nil {
			log.Printf("the upgrade's answer names no pending config: %v", err)
		}
		if p == nil {
			return nil
		}
		return &nodev1.PendingOutcome{Version: p.Version, Fate: nodev1.PendingFate_PENDING_FATE_KEPT}
	}
	kept := &nodev1.PendingOutcome{Version: c.pending, Fate: nodev1.PendingFate_PENDING_FATE_KEPT}
	if err := slot.Remove(slot.Pending(s.Loader.Slots)); err != nil {
		log.Printf("the pending config %s stays, although %x boots next with its config: remove it: %v", c.pending, rootHash, err)
		return kept
	}
	if c.from == fromPending {
		log.Printf("the pending config %s went to %x", c.pending, rootHash)
		return &nodev1.PendingOutcome{Version: c.pending, Fate: nodev1.PendingFate_PENDING_FATE_TAKEN}
	}
	log.Printf("the pending config %s is dropped: the upgrade gave %x other config", c.pending, rootHash)
	return &nodev1.PendingOutcome{Version: c.pending, Fate: nodev1.PendingFate_PENDING_FATE_REPLACED}
}
