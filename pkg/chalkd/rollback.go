package chalkd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
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

// RollbackImage makes the previous image boot next again, through the upgrade path without
// writing its store, and reboots into it when asked to. It runs under the lock an upgrade holds,
// so neither writes the ESP while the other does. An image whose config slot STATE no longer
// keeps is refused first: it would boot without config, fail its health check and fall back.
func (s *Server) RollbackImage(ctx context.Context, req *connect.Request[nodev1.RollbackImageRequest]) (*connect.Response[nodev1.RollbackImageResponse], error) {
	m := req.Msg
	if len(m.RootHash) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the request names no image to roll back to")
	}
	if !s.upgrading.TryLock() {
		return nil, failed(connect.CodeAborted, "an upgrade or a config delivery is running")
	}
	defer s.upgrading.Unlock()
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if !bytes.Equal(running, m.RootHash) {
		s.mu.Lock()
		kept := s.keepsSlot(m.RootHash)
		s.mu.Unlock()
		if !kept {
			return nil, failed(connect.CodeFailedPrecondition, "STATE holds no config for the image %x, so it would boot without config", m.RootHash)
		}
	}
	// The record is written once the rollback's checks passed, before the rename that stages the
	// image, so a refused rollback keeps the record of the last one. Status reports it only while
	// the node runs or stages the image, so a rename that fails leaves it unreported.
	record := func() error {
		if err := atomicfile.WriteFile(filepath.Join(s.Paths.StateDir, rollbackRecord), []byte(hex.EncodeToString(m.RootHash)+"\n"), 0o644); err != nil {
			return fmt.Errorf("record the rollback: %w", err)
		}
		return nil
	}
	res, err := s.reactivate(ctx, m.RootHash, record)
	if err != nil {
		log.Printf("rollback to %x failed: %v", m.RootHash, err)
		return nil, failed(connect.CodeFailedPrecondition, "roll back: %v", err)
	}
	resp := &nodev1.RollbackImageResponse{AlreadyInstalled: res.AlreadyInstalled, AlreadyStaged: res.AlreadyStaged, Entry: res.Entry}
	switch {
	case res.AlreadyInstalled:
		log.Printf("%x runs already", m.RootHash)
		return connect.NewResponse(resp), nil
	case res.AlreadyStaged:
		log.Printf("%x boots next already, as %s", m.RootHash, res.Entry)
	default:
		log.Printf("rolling back to %x, which boots next as %s", m.RootHash, res.Entry)
	}
	if m.Reboot {
		log.Printf("rebooting into %x", m.RootHash)
		resp.Rebooting = true
		s.rebootSoon()
	}
	return connect.NewResponse(resp), nil
}

// rollbackRecord is where STATE keeps the root hash of the image the last RollbackImage rolled back
// to, below its mount point.
const rollbackRecord = "chalkd/rollback"

// rolledBackTo is the root hash the rollback record names; nil without a record, or with one that
// cannot be read, which is logged: the record is reported, never relied on by the node.
func (s *Server) rolledBackTo() []byte {
	data, err := os.ReadFile(filepath.Join(s.Paths.StateDir, rollbackRecord))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var h []byte
	if err == nil {
		h, err = hex.DecodeString(strings.TrimSpace(string(data)))
	}
	if err != nil {
		log.Printf("read the record of the last rollback: %v", err)
		return nil
	}
	return h
}

// forgetRollback removes the rollback record once an upgrade installed an image.
func (s *Server) forgetRollback() {
	if err := os.Remove(filepath.Join(s.Paths.StateDir, rollbackRecord)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("remove the record of the last rollback: %v", err)
	}
}

// reactivate makes the image the inactive slot holds, by its root hash, boot next again; record
// is called once the rollback's checks passed, before the image is staged.
func (s *Server) reactivate(ctx context.Context, rootHash []byte, record func() error) (upgrade.Result, error) {
	if s.ReactivateImage != nil {
		return s.ReactivateImage(ctx, rootHash, record)
	}
	n, err := s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, err
	}
	return n.Rollback(ctx, rootHash, s.rolledBackTo(), record)
}

// keepsSlot reports whether STATE keeps a config slot of the image whose store has the root hash,
// which the image boots with.
func (s *Server) keepsSlot(rootHash []byte) bool {
	_, err := os.Lstat(slot.Dir(s.Loader.Slots, rootHash))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("look for the config slot of %x: %v", rootHash, err)
	}
	return err == nil
}
