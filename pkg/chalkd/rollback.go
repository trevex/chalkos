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

	"connectrpc.com/connect/v2"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/uki"
)

// RollbackConfig delivers the running image's previous config again. Like ApplyConfig it waits for
// no upgrade, which writes config slots too. During a trial it takes only the config from before
// the trial and ends the trial; a slot that runs that config already, as a failed rollback at the
// deadline leaves it, gets it delivered again, which finishes that rollback. With a trial record
// that cannot be read, it rolls back to the version asked for, or delivers it again when the slot
// runs it already, and then removes the record.
func (s *Server) RollbackConfig(ctx context.Context, req *nodev1.RollbackConfigRequest) (*nodev1.RollbackConfigResponse, error) {
	version := req.Version
	if version == "" {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the request names no version to roll back to")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.upgrading.TryLock() {
		return nil, layer.Errorf(connect.CodeAborted, "an upgrade, an image rollback or the rollback at a trial's deadline is running; roll back once it ended")
	}
	defer s.upgrading.Unlock()
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		// A record that cannot be read refuses every other change, and a rollback is where every
		// trial may end, so it goes once the config is rolled back. A slot that runs the version
		// asked for already, as a rollback at the deadline that failed after it wrote the slot
		// leaves it, has it delivered again, which finishes that rollback.
		log.Printf("read the record of the config's trial: %v; removing it once the config is rolled back", err)
		resp, err := s.rollbackOrRedeliver(ctx, version)
		if err != nil {
			return nil, err
		}
		return s.endTrialRolledBack(resp, "every change is refused until a rollback removes it"), nil
	}
	if !open {
		resp, _, err := s.rollbackConfig(ctx, version)
		if err != nil {
			return nil, err
		}
		return rollbackResponse(resp), nil
	}
	if version != r.Previous {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "config %s is on trial, so a rollback delivers config %s from before it, not %s", r.Version, r.Previous, version)
	}
	var resp *nodev1.ApplyConfigResponse
	if _, st, serr := r.state(s.Loader.Slots); serr == nil && st == trialEnded {
		log.Printf("the node runs config %s from before the trial of config %s; delivering it again to finish the rollback", r.Previous, r.Version)
		resp, err = s.redeliver(ctx)
	} else {
		resp, _, err = s.rollbackConfig(ctx, version)
	}
	if err != nil {
		return nil, err
	}
	return s.endTrialRolledBack(resp, "Status shows the trial until the next delivery, upgrade or image rollback, the trial's deadline or chalkd's next start removes the record"), nil
}

// rollbackOrRedeliver rolls the config back to version, or delivers it again when the running
// image's slot runs it already. The caller holds s.mu and s.upgrading.
func (s *Server) rollbackOrRedeliver(ctx context.Context, version string) (*nodev1.ApplyConfigResponse, error) {
	if _, running, err := s.runningVersion(); err == nil && running == version {
		log.Printf("the node runs config %s already; delivering it again", version)
		return s.redeliver(ctx)
	}
	resp, _, err := s.rollbackConfig(ctx, version)
	return resp, err
}

// endTrialRolledBack ends the trial once a rollback on request, which resp describes, went
// through: the node runs the config from before it again. A record that cannot be removed stays,
// and the warning says so and, after, what follows from that. The caller holds s.mu.
func (s *Server) endTrialRolledBack(resp *nodev1.ApplyConfigResponse, after string) *nodev1.RollbackConfigResponse {
	if err := s.endTrial(); err != nil {
		log.Printf("end the trial: %v", err)
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("the config was rolled back, but the record of its trial could not be removed for good: %v; %s", err, after))
	} else {
		log.Print("the trial ended with the rollback")
	}
	return rollbackResponse(resp)
}

// rollbackResponse is RollbackConfig's response to a rollback that resp describes.
func rollbackResponse(resp *nodev1.ApplyConfigResponse) *nodev1.RollbackConfigResponse {
	return &nodev1.RollbackConfigResponse{Changes: resp.Changes, RestartedUnits: resp.RestartedUnits, Warnings: resp.Warnings}
}

// rollbackConfig delivers the previous config of the running image's slot, which must be of
// version, through the whole pipeline of a delivery, translated again against the running image;
// the config it replaces becomes the slot's previous one. Only the documents return: the OS CAs,
// the node certificate and the layers' payloads are the node's, not the slot's. changed reports
// whether a failed rollback had begun to change the node. The caller holds s.mu and s.upgrading.
func (s *Server) rollbackConfig(ctx context.Context, version string) (resp *nodev1.ApplyConfigResponse, changed bool, err error) {
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, false, fmt.Errorf("find the running image's config slot: %w", err)
	}
	prev, found, err := slot.ReadPrevious(dir)
	switch {
	case err != nil:
		return nil, false, fmt.Errorf("read the previous config: %w", err)
	case !found:
		return nil, false, layer.Errorf(connect.CodeFailedPrecondition, "the node keeps no previous config of the running image: it keeps one once a delivery replaced the config the image was installed or upgraded with")
	case prev.Version != version:
		return nil, false, layer.Errorf(connect.CodeFailedPrecondition, "the node's previous config is %s, not %s", prev.Version, version)
	}
	log.Printf("rolling back to the previous config %s", version)
	return s.deliverStored(ctx, prev, "the previous config")
}

// redeliver delivers the config the running image's slot runs again, as the end of a trial whose
// slot runs the config from before it does. The effects compare with the baseline a delivery that
// failed part way left, so they take what it left undone, such as a restart or an output. The
// caller holds s.mu and s.upgrading.
func (s *Server) redeliver(ctx context.Context) (*nodev1.ApplyConfigResponse, error) {
	_, running, found, err := s.runningSlot()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "STATE holds no config of the running image")
	}
	resp, _, err := s.deliverStored(ctx, running, "the running config")
	return resp, err
}

// deliverStored delivers a config set a slot keeps to the running image's slot through the
// pipeline of a delivery, as rollbackConfig says; what names the set in a refusal. The slot is not
// written again when it runs the set already. changed reports whether a delivery that failed did
// so after it began to change the node, which another delivery then finishes.
func (s *Server) deliverStored(ctx context.Context, stored slot.Stored, what string) (resp *nodev1.ApplyConfigResponse, changed bool, err error) {
	name, err := s.name()
	if err != nil {
		return nil, false, err
	}
	t, schemas, err := s.runningTarget()
	if err != nil {
		return nil, false, err
	}
	next, err := translateSlot(name, stored.Delivered, schemas, t)
	if err != nil {
		return nil, false, layer.Errorf(connect.CodeFailedPrecondition, "%s no longer suits the running image: %w", what, err)
	}
	d, err := s.planDelivery(ctx, applyRequest{identity: string(next.Identity), next: next, msg: &nodev1.ApplyConfigRequest{}})
	if err != nil {
		return nil, false, err
	}
	defer d.staged.Discard()
	d.held = d.replaced == next.Version
	r, started, err := d.commit(ctx)
	if err != nil {
		return nil, started, err
	}
	return r, false, nil
}

// RollbackImage makes the previous image boot next again, through the upgrade path without
// writing its store, and reboots into it when asked to. It runs under the lock an upgrade holds,
// so neither writes the ESP while the other does. An image whose config slot STATE no longer
// keeps is refused first: it would boot without config, fail its health check and fall back.
func (s *Server) RollbackImage(ctx context.Context, m *nodev1.RollbackImageRequest) (*nodev1.RollbackImageResponse, error) {
	if len(m.RootHash) == 0 {
		return nil, layer.Errorf(connect.CodeInvalidArgument, "the request names no image to roll back to")
	}
	unlock, err := s.lockSlots("an image rollback")
	if err != nil {
		return nil, err
	}
	defer unlock()
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(running, m.RootHash) {
		s.mu.Lock()
		kept := s.keepsSlot(m.RootHash)
		s.mu.Unlock()
		if !kept {
			return nil, layer.Errorf(connect.CodeFailedPrecondition, "STATE holds no config for the image %x, so it would boot without config", m.RootHash)
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
	res, err := s.imageSlots().Reactivate(ctx, m.RootHash, record)
	if err != nil {
		log.Printf("rollback to %x failed: %v", m.RootHash, err)
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "roll back: %w", err)
	}
	resp := &nodev1.RollbackImageResponse{AlreadyInstalled: res.AlreadyInstalled, AlreadyStaged: res.AlreadyStaged, Entry: res.Entry}
	switch {
	case res.AlreadyInstalled:
		log.Printf("%x runs already", m.RootHash)
		return resp, nil
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
	return resp, nil
}

// rollbackRecord is where STATE keeps the root hash of the image the last RollbackImage rolled back
// to, below its mount point.
const rollbackRecord = "chalkd/rollback"

// rolledBackTo is the root hash the rollback record names; nil without a record, or with one that
// cannot be read, which is logged. The record is reported, and it lets a repeated rollback find
// its counted entry staged; an unreadable one counts as none, which refuses that repeat.
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

// keepsSlot reports whether STATE keeps a config slot of the image whose store has the root hash,
// which the image boots with.
func (s *Server) keepsSlot(rootHash []byte) bool {
	_, err := os.Lstat(slot.Dir(s.Loader.Slots, rootHash))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("look for the config slot of %x: %v", rootHash, err)
	}
	return err == nil
}
