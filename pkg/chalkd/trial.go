package chalkd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config/slot"
)

// trialFile is where STATE keeps the record of a config on trial, below its mount point.
const trialFile = "chalkd/trial"

// trialRecord is a config on trial: the slot it was delivered to, by its name below the slots'
// directory; the version on trial; the version the slot ran before, which the slot keeps as its
// previous config and a rollback delivers again; the deadline; and the units the trial's health
// check leaves out besides the image's. Error is why the last rollback at the deadline failed.
type trialRecord struct {
	Slot        string    `json:"slot"`
	Version     string    `json:"version"`
	Previous    string    `json:"previous"`
	Deadline    time.Time `json:"deadline"`
	IgnoreUnits []string  `json:"ignoreUnits,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// readTrial reads the trial record below stateDir; open is false without one.
func readTrial(stateDir string) (r trialRecord, open bool, err error) {
	data, err := os.ReadFile(filepath.Join(stateDir, trialFile))
	if errors.Is(err, fs.ErrNotExist) {
		return trialRecord{}, false, nil
	}
	if err != nil {
		return trialRecord{}, false, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return trialRecord{}, false, fmt.Errorf("the trial record cannot be read: %w", err)
	}
	if h, err := hex.DecodeString(r.Slot); err != nil || len(h) == 0 || r.Version == "" || r.Previous == "" {
		return trialRecord{}, false, errors.New("the trial record names no slot and versions")
	}
	if r.Deadline.IsZero() {
		return trialRecord{}, false, errors.New("the trial record names no deadline")
	}
	return r, true, nil
}

// sameTrial reports whether r and o record the same trial, with the same deadline and ignored
// units. The error is left out: a failed rollback's retry rewrites it.
func (r trialRecord) sameTrial(o trialRecord) bool {
	return r.Slot == o.Slot && r.Version == o.Version && r.Previous == o.Previous && r.Deadline.Equal(o.Deadline) && slices.Equal(r.IgnoreUnits, o.IgnoreUnits)
}

// writeTrial writes the trial record below stateDir and syncs it, so it survives a crash once
// writeTrial returns.
func writeTrial(stateDir string, r trialRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(stateDir, trialFile), append(data, '\n'), 0o644)
}

// removeTrial removes the trial record and syncs its directory, so that after a power cut no boot
// rolls back a config whose trial ended.
func removeTrial(stateDir string) error {
	path := filepath.Join(stateDir, trialFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(path))
}

// trialState is where a trial stands, as its slot shows it.
type trialState int

const (
	// trialApplied: the slot runs the config on trial and keeps the one from before it as its
	// previous config, which a rollback delivers again.
	trialApplied trialState = iota
	// trialEnded: the slot runs the config from before the trial. The trial was never applied, as
	// after a crash before its slot was written, or was rolled back.
	trialEnded
	// trialLost: the slot runs neither, or keeps another previous config.
	trialLost
)

// state reads where the trial stands from its slot below slots, and returns the slot. A previous
// link that names the set the slot holds, as an interrupted write leaves it, is no previous config.
func (r trialRecord) state(slots string) (string, trialState, error) {
	dir := filepath.Join(slots, r.Slot)
	running, err := slot.Version(dir)
	if err != nil {
		return dir, trialLost, err
	}
	previous, err := slot.Version(slot.Previous(dir))
	if err != nil {
		return dir, trialLost, err
	}
	switch {
	case running == r.Previous:
		return dir, trialEnded, nil
	case running == r.Version && previous == r.Previous:
		return dir, trialApplied, nil
	}
	return dir, trialLost, nil
}

// ConfirmConfig ends the trial of a config once the node is healthy, and the node keeps the config.
// The health check leaves out the units the trial's record names besides the image's. It runs
// before s.mu is taken: the layers' checks take their time and must not hold back a delivery or
// the deadline. The record is read without the lock first, and a request that cannot confirm it,
// for another version or without a trial, skips the check. Under the lock the record is read
// again, and a record that changed meanwhile is refused with UNAVAILABLE, so the client asks again
// and the check covers the trial it confirms. A trial past its deadline is confirmed only while
// its rollback failed and is retried.
func (s *Server) ConfirmConfig(ctx context.Context, req *connect.Request[nodev1.ConfirmConfigRequest]) (*connect.Response[nodev1.ConfirmConfigResponse], error) {
	version := req.Msg.Version
	if version == "" {
		return nil, failed(connect.CodeInvalidArgument, "the request names no config version to confirm")
	}
	// A record that cannot be read here is reported under the lock.
	checked, open, err := readTrial(s.Paths.StateDir)
	if err == nil && open && checked.Version == version && s.Healthy != nil {
		if err := s.Healthy(ctx, checked.IgnoreUnits); err != nil {
			return nil, failed(connect.CodeUnavailable, "the node is not healthy: %v", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the record of the config's trial: %v", err)
	}
	if !open {
		dir, err := s.Loader.Slot()
		if err != nil {
			return nil, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
		}
		running, err := slot.Version(dir)
		if err != nil {
			return nil, failed(connect.CodeInternal, "read the config's version: %v", err)
		}
		// A confirmation sent again, whose first answer was lost, finds the node on the config.
		if running == version {
			return connect.NewResponse(&nodev1.ConfirmConfigResponse{}), nil
		}
		return nil, failed(connect.CodeFailedPrecondition, "no trial is open, and the node runs config %s, not %s", running, version)
	}
	if r.Version != version {
		return nil, failed(connect.CodeFailedPrecondition, "the trial is of config %s, not %s", r.Version, version)
	}
	if !r.sameTrial(checked) {
		return nil, failed(connect.CodeUnavailable, "the trial changed while the node's health was checked")
	}
	if !time.Now().Before(r.Deadline) && r.Error == "" {
		return nil, failed(connect.CodeFailedPrecondition, "the trial of config %s ended at its deadline, %s", r.Version, r.Deadline.UTC().Format(time.RFC3339))
	}
	if _, st, err := r.state(s.Loader.Slots); err != nil {
		return nil, failed(connect.CodeInternal, "read the trial's config slot: %v", err)
	} else if st != trialApplied {
		return nil, failed(connect.CodeFailedPrecondition, "the node does not run config %s on trial with config %s as its previous config", r.Version, r.Previous)
	}
	if err := s.endTrial(); err != nil {
		return nil, failed(connect.CodeInternal, "end the trial: %v", err)
	}
	log.Printf("config %s was confirmed; its trial ended", version)
	return connect.NewResponse(&nodev1.ConfirmConfigResponse{}), nil
}

// endTrial removes the trial's record and then stops its deadline; the caller holds s.mu. A record
// that cannot be removed keeps the deadline armed, which finds the trial ended and removes it.
func (s *Server) endTrial() error {
	if err := removeTrial(s.Paths.StateDir); err != nil {
		return err
	}
	if s.trialTimer != nil {
		s.trialTimer.Stop()
		s.trialTimer = nil
	}
	return nil
}

// trialStatus is the config on trial as Status reports it; nil without one. A record that cannot
// be read is logged and left out.
func (s *Server) trialStatus() *nodev1.ConfigTrial {
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		log.Printf("read the record of the config's trial: %v", err)
		return nil
	}
	if !open {
		return nil
	}
	return &nodev1.ConfigTrial{Version: r.Version, PreviousVersion: r.Previous, Deadline: timestamppb.New(r.Deadline), Error: r.Error, IgnoreUnits: r.IgnoreUnits}
}

// refuseOnTrial refuses what would change the node while a config is on trial, so the trial's
// rollback restores what the node ran before it. what names the change. A record whose slot runs
// the config from before the trial already, as a rollback whose record a crash or a failed removal
// kept leaves it, is removed, and the change goes ahead. The caller holds s.mu, which a trial's
// delivery holds while it writes the record; an upgrade and an image rollback hold s.upgrading too.
func (s *Server) refuseOnTrial(what string) error {
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		return failed(connect.CodeInternal, "read the record of the config's trial: %v", err)
	}
	if !open {
		return nil
	}
	if _, st, err := r.state(s.Loader.Slots); err != nil {
		log.Printf("read the trial's config slot: %v", err)
	} else if st == trialEnded {
		if err := s.endTrial(); err != nil {
			return failed(connect.CodeInternal, "remove the record of the ended trial of config %s: %v", r.Version, err)
		}
		log.Printf("the trial of config %s ended: the node runs config %s already", r.Version, r.Previous)
		return nil
	}
	name := s.nodeName()
	return failed(connect.CodeFailedPrecondition, "config %s is on trial until %s: confirm it with chalkctl apply-config --confirm %s, or roll it back with chalkctl apply-config --rollback %s, before %s",
		r.Version, r.Deadline.UTC().Format(time.RFC3339), name, name, what)
}
