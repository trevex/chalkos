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
	"regexp"
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
// check leaves out besides the image's. Error is why the last rollback at the deadline failed,
// Attempts counts the rollbacks at the deadline that failed, and Changed is set once one failed
// after it changed the node, which a rollback on request then finishes.
type trialRecord struct {
	Slot        string    `json:"slot"`
	Version     string    `json:"version"`
	Previous    string    `json:"previous"`
	Deadline    time.Time `json:"deadline"`
	IgnoreUnits []string  `json:"ignoreUnits,omitempty"`
	Error       string    `json:"error,omitempty"`
	Attempts    int       `json:"attempts,omitempty"`
	Changed     bool      `json:"changed,omitempty"`
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
// units, and with no rollback at the deadline attempted in between.
func (r trialRecord) sameTrial(o trialRecord) bool {
	return r.Slot == o.Slot && r.Version == o.Version && r.Previous == o.Previous && r.Deadline.Equal(o.Deadline) && slices.Equal(r.IgnoreUnits, o.IgnoreUnits) &&
		r.Error == o.Error && r.Attempts == o.Attempts && r.Changed == o.Changed
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
// and the check covers the trial it confirms; so is a record a rollback at the deadline rewrote.
// A trial past its deadline is confirmed only while its rollback failed and is retried, and not
// once a rollback failed after it changed the node.
func (s *Server) ConfirmConfig(ctx context.Context, req *connect.Request[nodev1.ConfirmConfigRequest]) (*connect.Response[nodev1.ConfirmConfigResponse], error) {
	version := req.Msg.Version
	if version == "" {
		return nil, failed(connect.CodeInvalidArgument, "the request names no config version to confirm")
	}
	// A record that cannot be read here is reported under the lock, and so is a trial that cannot
	// be confirmed.
	checked, open, err := readTrial(s.Paths.StateDir)
	confirmable := (time.Now().Before(checked.Deadline) || checked.Error != "") && !checked.Changed
	if err == nil && open && checked.Version == version && confirmable && s.Healthy != nil {
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
	if r.Changed {
		return nil, failed(connect.CodeFailedPrecondition, "the rollback of config %s at its trial's deadline failed after it changed the node: %s; finish it with chalkctl apply-config --rollback %s", r.Version, r.Error, s.nodeName())
	}
	_, st, err := r.state(s.Loader.Slots)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the trial's config slot: %v", err)
	}
	if st != trialApplied {
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

// maxTrial is the longest trial: one longer would keep the node from every other change for too
// long.
const maxTrial = 24 * time.Hour

// unitName is a systemd unit's name: its characters, and a suffix of a unit type.
var unitName = regexp.MustCompile(`^[A-Za-z0-9:_.@\\-]+\.(service|socket|device|mount|automount|swap|target|path|timer|slice|scope)$`)

// trialOf is how long a request's config is on trial, zero without a trial. A trial delivers
// config to the running image alone: what a rollback leaves as it is goes without one. The units
// its health check leaves out must be unit names, which the check compares with systemctl's.
func trialOf(m *nodev1.ApplyConfigRequest) (time.Duration, error) {
	if m.Try == nil {
		if len(m.IgnoreUnits) > 0 {
			return 0, failed(connect.CodeInvalidArgument, "ignore_units goes with try")
		}
		return 0, nil
	}
	d := m.Try.AsDuration()
	switch {
	case m.Config == "" || m.Staged:
		return 0, failed(connect.CodeInvalidArgument, "a trial delivers config to the running image")
	case len(m.Layers) > 0 || len(m.NodeCertificate) > 0 || len(m.Trust) > 0:
		return 0, failed(connect.CodeInvalidArgument, "a trial delivers config alone: a rollback keeps the OS CAs, the node certificate and the layers' payloads, so deliver them without a trial")
	case !m.Try.IsValid() || d <= 0 || d > maxTrial:
		return 0, failed(connect.CodeInvalidArgument, "a trial lasts more than 0s and at most %v, not %v", maxTrial, d)
	}
	for _, u := range m.IgnoreUnits {
		if len(u) > 255 || !unitName.MatchString(u) {
			return 0, failed(connect.CodeInvalidArgument, "%q is no unit name, such as demo.service", u)
		}
	}
	return d, nil
}

// checkTrial refuses a trial that its rollback could not undo whole: of a slot without config to
// roll back to, of the config the slot runs, or of a config that changes the machine's storage.
func checkTrial(found bool, running, next string, m machineChange) error {
	switch {
	case !found || running == "":
		return failed(connect.CodeFailedPrecondition, "STATE holds no config of the running image to roll back to; deliver the config without a trial")
	case running == next:
		return failed(connect.CodeFailedPrecondition, "the node runs config %s already; a trial needs a config that changes it", next)
	case len(m.changes) > 0:
		c := m.changes[0]
		return failed(connect.CodeFailedPrecondition, "the config changes the node's storage, volume %s: %s, which a rollback cannot undo; deliver it without a trial", c.Volume, c.Reason)
	}
	return nil
}

// applyTrial delivers config on trial. The record goes to STATE, synced, before the delivery
// changes anything, so neither a crash nor a boot finds the config on trial without it; once the
// delivery ended, settleTrial arms the deadline or, when the slot does not run the config, drops
// the record. The caller holds s.mu and s.upgrading.
func (s *Server) applyTrial(ctx context.Context, r applyRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	d, err := s.planDelivery(ctx, r)
	if err != nil {
		return nil, err
	}
	defer d.staged.Discard()
	rec := trialRecord{Slot: filepath.Base(d.dir), Version: d.stored.Version, Previous: d.replaced, Deadline: time.Now().Add(r.try), IgnoreUnits: r.msg.IgnoreUnits}
	if err := writeTrial(s.Paths.StateDir, rec); err != nil {
		s.refused(d.fx)
		return nil, failed(connect.CodeInternal, "record the trial: %v", err)
	}
	resp, err := d.commit(ctx)
	s.settleTrial(rec)
	if err != nil {
		return nil, err
	}
	log.Printf("config %s is on trial until %s; it rolls back to config %s unless confirmed", rec.Version, rec.Deadline.UTC().Format(time.RFC3339), rec.Previous)
	return resp, nil
}

// settleTrial arms the deadline of the trial while its slot runs the config on trial, and removes
// the record otherwise: of a delivery that failed before it wrote the slot, or of a trial that
// ended before chalkd started. A slot that cannot be read leaves the decision to the deadline. The
// caller holds s.mu.
func (s *Server) settleTrial(r trialRecord) {
	_, st, err := r.state(s.Loader.Slots)
	switch {
	case err != nil:
		log.Printf("read the config of the trial's slot: %v; its deadline decides", err)
		s.armTrial(r, time.Until(r.Deadline))
	case st == trialApplied:
		s.armTrial(r, time.Until(r.Deadline))
	default:
		if err := s.endTrial(); err != nil {
			log.Printf("remove the record of the config's trial: %v", err)
		}
	}
}

// armTrial has the trial expire after the time given, at once when it is not positive; the
// caller holds s.mu.
func (s *Server) armTrial(r trialRecord, after time.Duration) {
	if s.trialTimer != nil {
		s.trialTimer.Stop()
	}
	s.trialTimer = time.AfterFunc(after, func() { s.expireTrial(r.Version, r.Deadline) })
}

// retryTrial is the time before a rollback at a trial's deadline that failed is tried again.
func (s *Server) retryTrial() time.Duration {
	if s.trialRetry > 0 {
		return s.trialRetry
	}
	return time.Minute
}

// expireTrial rolls back the config on trial whose version and deadline are given, once the
// deadline passed unconfirmed, through rollbackConfig, the pipeline of RollbackConfig. It takes
// s.upgrading before s.mu, as Upgrade and RollbackImage do. ApplyConfig and RollbackConfig hold
// s.mu and only try s.upgrading, so nothing waits for the lock the other holds. The trial is
// re-read under the locks:
//   - a trial confirmed or rolled back meanwhile, or a later trial, even of the same version, is
//     left alone;
//   - a node that runs the config from before the trial already, as after a rollback on request
//     whose record a crash kept, only loses the record, and so does a slot that runs neither;
//   - a rollback that fails keeps the trial open, its failure and whether it changed the node in
//     the record, and is tried again.
//
// The rollback outlives every request, so it runs with a context of its own.
func (s *Server) expireTrial(version string, deadline time.Time) {
	s.upgrading.Lock()
	defer s.upgrading.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, open, err := readTrial(s.Paths.StateDir)
	switch {
	case err != nil:
		log.Printf("read the record of the config's trial: %v; trying again in %v", err, s.retryTrial())
		s.armTrial(trialRecord{Version: version, Deadline: deadline}, s.retryTrial())
		return
	case !open || r.Version != version || !r.Deadline.Equal(deadline):
		return
	}
	_, st, err := r.state(s.Loader.Slots)
	changed := false
	if err == nil && st == trialApplied {
		log.Printf("config %s was not confirmed by its trial's deadline; rolling back to config %s", r.Version, r.Previous)
		_, changed, err = s.rollbackConfig(context.Background(), r.Previous)
	}
	if err != nil {
		log.Printf("the rollback of config %s at its trial's deadline failed: %v; trying again in %v", r.Version, err, s.retryTrial())
		r.Error, r.Attempts, r.Changed = plain(err).Error(), r.Attempts+1, r.Changed || changed
		if werr := writeTrial(s.Paths.StateDir, r); werr != nil {
			log.Printf("record the failed rollback: %v", werr)
		}
		s.armTrial(r, s.retryTrial())
		return
	}
	switch st {
	case trialEnded:
		log.Printf("the trial of config %s ended: the node runs config %s already", r.Version, r.Previous)
	case trialLost:
		log.Printf("the trial of config %s ended: its slot runs neither it nor config %s", r.Version, r.Previous)
	}
	if err := s.endTrial(); err != nil {
		log.Printf("remove the record of the config's trial: %v", err)
	}
}

// ResumeTrial arms the deadline of a trial recorded before chalkd started: one past its deadline
// rolls back at once. A record of a trial whose slot does not run its config, as a crash before
// the slot was written leaves it, goes, as does a record that cannot be read: no trial is open
// then. chalkd calls it in normal mode before it serves.
func (s *Server) ResumeTrial() {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		log.Printf("read the record of the config's trial: %v; removing it, so no trial is open", err)
		if err := s.endTrial(); err != nil {
			log.Printf("remove the record of the config's trial: %v", err)
		}
		return
	}
	if open {
		log.Printf("config %s is on trial until %s", r.Version, r.Deadline.UTC().Format(time.RFC3339))
		s.settleTrial(r)
	}
}
