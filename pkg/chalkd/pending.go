package chalkd

import (
	"fmt"
	"log"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
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
	since, _, err := slot.Written(dir)
	if err != nil {
		return nil, fmt.Errorf("read when the pending config was stored: %w", err)
	}
	return &nodev1.PendingConfig{Version: version, Since: timestamppb.New(since)}, nil
}
