package chalkd

import (
	"context"
	"log"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config/slot"
)

// RollbackConfig delivers the running image's previous config again, as rollbackConfig does. Like
// ApplyConfig, it waits for no upgrade: one writes the node's config slots too.
func (s *Server) RollbackConfig(ctx context.Context, req *connect.Request[nodev1.RollbackConfigRequest]) (*connect.Response[nodev1.RollbackConfigResponse], error) {
	if req.Msg.Version == "" {
		return nil, failed(connect.CodeInvalidArgument, "the request names no version to roll back to")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.upgrading.TryLock() {
		return nil, failed(connect.CodeAborted, "an upgrade is running, which writes the node's config too; roll back once it ended")
	}
	defer s.upgrading.Unlock()
	resp, err := s.rollbackConfig(ctx, req.Msg.Version)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nodev1.RollbackConfigResponse{Changes: resp.Changes, RestartedUnits: resp.RestartedUnits, Warnings: resp.Warnings}), nil
}

// rollbackConfig delivers the previous config of the running image's slot, whose version must be
// version, through apply: checked by the image's rules against the config the node runs, the
// machine and the facts, rendered, written and acted on by the effects as any delivery, and
// refused as a whole when anything refuses it. The config it replaces becomes the slot's previous
// one. Only the documents return: the OS CAs, the node certificate and the layers' payloads are
// the node's, not the slot's. The documents are translated again, as delivered, against the
// running image. The caller holds s.mu and s.upgrading.
func (s *Server) rollbackConfig(ctx context.Context, version string) (*nodev1.ApplyConfigResponse, error) {
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
	}
	running, err := slot.Version(dir)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the config's version: %v", err)
	}
	prev, found, err := slot.Read(slot.Previous(dir))
	switch {
	case err != nil:
		return nil, failed(connect.CodeInternal, "read the previous config: %v", err)
	// A write interrupted between the previous link and the slot's leaves both naming one set,
	// which is no config to roll back to.
	case !found || prev.Version == "" || prev.Version == running:
		return nil, failed(connect.CodeFailedPrecondition, "the node keeps no previous config of the running image: it keeps one once a delivery replaced the config the image was installed or upgraded with")
	case prev.Version != version:
		return nil, failed(connect.CodeFailedPrecondition, "the node's previous config is %s, not %s", prev.Version, version)
	}
	name, err := s.name()
	if err != nil {
		return nil, err
	}
	t, schemas, err := s.runningTarget()
	if err != nil {
		return nil, err
	}
	next, err := translateSlot(name, prev.Delivered, schemas, t)
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "the previous config no longer suits the running image: %v", err)
	}
	log.Printf("rolling back to the previous config %s", version)
	resp, err := s.apply(ctx, applyRequest{identity: string(next.Identity), next: next, schemas: schemas, msg: &nodev1.ApplyConfigRequest{}})
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
