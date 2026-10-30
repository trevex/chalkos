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
// check leaves out besides the image's. Error is why the last attempt to roll the trial back
// failed, at the deadline or as a delivery of the config from before it, which the node runs
// already, that ends the trial, or why the trial's slot could not be read at the deadline;
// Attempts counts those that failed, and Changed is set once one failed after it changed the
// node, which the deadline's retries or a rollback on request then finish.
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
		return trialRecord{}, false, fmt.Errorf("parse the trial record: %w", err)
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
		return nil, s.unreadableTrial(err)
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
		return nil, failed(connect.CodeFailedPrecondition, "a rollback of config %s failed after it changed the node: %s; chalkd tries again each minute, or chalkctl apply-config --rollback %s finishes it now", r.Version, r.Error, s.nodeName())
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
// that cannot be removed keeps the deadline, if one is armed, so the trial is not lost.
func (s *Server) endTrial() error {
	if err := removeTrial(s.Paths.StateDir); err != nil {
		return err
	}
	if s.trialTimer != nil {
		s.trialTimer.Stop()
		s.trialTimer = nil
	}
	// A deadline that fired already and waits for the locks finds it was stopped.
	s.trialArmed++
	return nil
}

// dropLostTrial ends the trial r, whose slot runs neither its config nor the one from before it,
// which leaves nothing to confirm or roll back: its record goes, as endTrial says. The caller
// holds s.mu.
func (s *Server) dropLostTrial(r trialRecord) error {
	log.Printf("the trial of config %s ended: its slot runs neither it nor config %s", r.Version, r.Previous)
	return s.endTrial()
}

// trialStatus is the config on trial as Status reports it, nil without one, and why its record
// cannot be read, which is logged and leaves the trial out.
func (s *Server) trialStatus() (*nodev1.ConfigTrial, string) {
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		log.Printf("read the record of the config's trial: %v", err)
		return nil, err.Error()
	}
	if !open {
		return nil, ""
	}
	return &nodev1.ConfigTrial{Version: r.Version, PreviousVersion: r.Previous, Deadline: timestamppb.New(r.Deadline), Error: r.Error, IgnoreUnits: r.IgnoreUnits, RollbackChanged: r.Changed}, ""
}

// unreadableTrial refuses a request for err, which kept the trial's record from being read. The
// record stays until RollbackConfig removes it: every trial may end in a rollback. chalkctl tells
// this refusal from other internal errors by its message's start, so it stays as it is.
func (s *Server) unreadableTrial(err error) error {
	return failed(connect.CodeInternal, "read the record of the config's trial: %v; other changes are refused until chalkctl apply-config --rollback %s restores the previous config, which removes the record", err, s.nodeName())
}

// refuseOnTrialLocking is refuseOnTrial under s.mu, for an upgrade and an image rollback, which
// hold s.upgrading and take s.mu only while they read or write what it guards.
func (s *Server) refuseOnTrialLocking(what string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refuseOnTrial(what, true)
}

// refuseOnTrial refuses what would change the node while a config is on trial, so the trial's
// rollback restores what the node ran before it. what names the change. A trial whose slot runs
// the config from before it already, as a rollback that failed after it wrote the slot, or whose
// record a crash or a failed removal kept, leaves it, is finished first, and the change goes
// ahead. That needs s.upgrading: holdsUpgrading tells that the caller holds it, and otherwise it
// is tried, and the change is refused with ABORTED while the upgrade, the image rollback or the
// rollback at a trial's deadline that holds it runs. A trial whose slot runs neither config loses
// its record, as at its deadline, and the change goes ahead. The caller holds s.mu, which a
// trial's delivery holds while it writes the record; an upgrade and an image rollback call it
// through refuseOnTrialLocking.
func (s *Server) refuseOnTrial(what string, holdsUpgrading bool) error {
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		return s.unreadableTrial(err)
	}
	if !open {
		return nil
	}
	if _, st, err := r.state(s.Loader.Slots); err != nil {
		log.Printf("read the trial's config slot: %v", err)
	} else if st == trialEnded {
		if !holdsUpgrading {
			if !s.upgrading.TryLock() {
				return failed(connect.CodeAborted, "the trial of config %s ended, and its record goes once the node delivered config %s again, which waits for the upgrade, the image rollback or the rollback at a trial's deadline that runs; try again once it ended", r.Version, r.Previous)
			}
			defer s.upgrading.Unlock()
		}
		return s.finishTrial(r)
	} else if st == trialLost {
		if err := s.dropLostTrial(r); err != nil {
			return failed(connect.CodeInternal, "remove the record of the lost trial of config %s: %v", r.Version, err)
		}
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
// delivery ended, settleTrial arms the deadline or, when the slot does not run the config, ends
// the trial. A delivery that failed says which: the config stays on trial until its deadline, or
// the config from before it is delivered again. The caller holds s.mu and s.upgrading.
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
	st, ferr := s.settleTrial(rec)
	if err == nil {
		log.Printf("config %s is on trial until %s; it rolls back to config %s unless confirmed", rec.Version, rec.Deadline.UTC().Format(time.RFC3339), rec.Previous)
		return resp, nil
	}
	switch {
	case st == trialApplied:
		return nil, failed(connect.CodeOf(err), "%v; config %s stays on trial until %s", plain(err), rec.Version, rec.Deadline.UTC().Format(time.RFC3339))
	case st == trialEnded && ferr != nil:
		return nil, failed(connect.CodeOf(err), "%v; %v", plain(err), plain(ferr))
	case st == trialEnded:
		return nil, failed(connect.CodeOf(err), "%v; config %s is delivered again, and no trial is open", plain(err), rec.Previous)
	}
	return nil, err
}

// settleTrial settles the trial r once its delivery ended or chalkd started, and returns where it
// stands: while its slot runs the config on trial, it arms the deadline; while the slot runs the
// config from before it, as a delivery that failed before it wrote the slot or a crash leaves it,
// it finishes the trial and returns finishTrial's error. A trial whose slot runs neither loses its
// record; one that cannot be removed has the deadline try again. A slot that cannot be read
// leaves the decision to the deadline, as the trial applied. The caller holds s.mu and
// s.upgrading.
func (s *Server) settleTrial(r trialRecord) (trialState, error) {
	_, st, err := r.state(s.Loader.Slots)
	switch {
	case err != nil:
		log.Printf("read the config of the trial's slot: %v; its deadline decides", err)
		s.armTrial(r, time.Until(r.Deadline))
		return trialApplied, nil
	case st == trialApplied:
		s.armTrial(r, time.Until(r.Deadline))
	case st == trialEnded:
		return st, s.finishTrial(r)
	default:
		if err := s.dropLostTrial(r); err != nil {
			// The deadline finds the trial lost again and removes the record then.
			log.Printf("remove the record of the config's trial: %v; trying again in %v", err, s.retryTrial())
			s.armTrial(r, s.retryTrial())
		}
	}
	return st, nil
}

// trialRollbackTimeout is the longest a rollback at a trial's deadline, or the delivery that
// finishes a trial's rollback, may take; one that takes longer failed, and is tried again.
const trialRollbackTimeout = 5 * time.Minute

// rollbackContext is the context of a rollback at a trial's deadline or the delivery that
// finishes one: they outlive every request, so it is a context of their own, with their timeout,
// which ends once chalkd stops as well. The caller holds s.mu.
func (s *Server) rollbackContext() (context.Context, context.CancelFunc) {
	d := s.trialTimeout
	if d <= 0 {
		d = trialRollbackTimeout
	}
	parent := s.trialCtx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, d)
}

// finishTrial ends a trial whose slot runs the config from before it again. It delivers that
// config again first: the effects compare with the baseline a delivery that failed part way left,
// the rollback's or the trial's own, so they take what it left undone, such as a restart or an
// output's swap. Only then does the record go. A delivery that fails keeps the trial open, as
// keepTrial does, and the deadline tries again; so does a record that cannot be removed. The
// caller holds s.mu and s.upgrading.
func (s *Server) finishTrial(r trialRecord) error {
	log.Printf("the node runs config %s from before the trial of config %s; delivering it again to end the trial", r.Previous, r.Version)
	ctx, cancel := s.rollbackContext()
	defer cancel()
	if _, err := s.redeliver(ctx); err != nil {
		s.keepTrial(r, err, true)
		return failed(connect.CodeOf(err), "the node runs config %s from before the trial of config %s, but delivering it again, which ends the trial, failed: %v; the trial stays open, and chalkd tries again each minute", r.Previous, r.Version, plain(err))
	}
	if err := s.endTrial(); err != nil {
		log.Printf("remove the record of the ended trial of config %s: %v; trying again in %v", r.Version, err, s.retryTrial())
		s.armTrial(r, s.retryTrial())
		return failed(connect.CodeInternal, "remove the record of the ended trial of config %s: %v", r.Version, err)
	}
	log.Printf("the trial of config %s ended: the node runs config %s", r.Version, r.Previous)
	return nil
}

// keepTrial keeps the trial r open after err failed its rollback at the deadline, or the delivery
// that finishes it: the record takes the failure, counts the attempt and whether it changed the
// node, and the deadline is armed again for the retry. The caller holds s.mu.
func (s *Server) keepTrial(r trialRecord, err error, changed bool) {
	log.Printf("the rollback of config %s failed: %v; trying again in %v", r.Version, err, s.retryTrial())
	r.Error, r.Attempts, r.Changed = plain(err).Error(), r.Attempts+1, r.Changed || changed
	if werr := writeTrial(s.Paths.StateDir, r); werr != nil {
		log.Printf("record the failed rollback: %v", werr)
	}
	s.armTrial(r, s.retryTrial())
}

// armTrial has the trial expire after the time given, at once when it is not positive; the
// caller holds s.mu. The record's deadline is wall-clock time, which outlives chalkd, while the
// timer counts on the monotonic clock from now: a step of the system's clock after the deadline
// was armed moves the timer no more, and chalkd's next start arms it from the recorded deadline.
func (s *Server) armTrial(r trialRecord, after time.Duration) {
	if s.trialTimer != nil {
		s.trialTimer.Stop()
	}
	s.trialArmed++
	armed := s.trialArmed
	s.trialTimer = time.AfterFunc(after, func() { s.expireTrial(r.Version, r.Deadline, armed) })
}

// retryTrial is the time before a rollback at a trial's deadline that failed is tried again.
func (s *Server) retryTrial() time.Duration {
	if s.trialRetry > 0 {
		return s.trialRetry
	}
	return time.Minute
}

// expireTrial rolls back the config on trial whose version and deadline are given, once the
// deadline passed unconfirmed, through rollbackConfig, the pipeline of RollbackConfig. armed is
// the count of armed deadlines when its own was armed: a deadline armed after it, or stopped,
// leaves it nothing to do, as its timer may have fired while it was replaced. It takes s.upgrading
// before s.mu, as Upgrade and RollbackImage do. ApplyConfig and RollbackConfig hold s.mu and only
// try s.upgrading, so nothing waits for the lock the other holds. The trial is re-read under the
// locks:
//   - a trial confirmed or rolled back meanwhile, or a later trial, even of the same version, is
//     left alone;
//   - a node that runs the config from before the trial already, as after a rollback that failed
//     after it wrote the slot, or a rollback on request whose record a crash kept, finishes the
//     trial; a slot that runs neither only loses the record;
//   - a rollback that fails keeps the trial open, its failure and whether it changed the node in
//     the record, and is tried again; so does one that outlasts trialRollbackTimeout.
func (s *Server) expireTrial(version string, deadline time.Time, armed uint64) {
	s.upgrading.Lock()
	defer s.upgrading.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	// A deadline that fires once chalkd stopped leaves the trial to the next start, so its record
	// takes no failure the stop caused.
	if armed != s.trialArmed || s.trialCtx != nil && s.trialCtx.Err() != nil {
		return
	}
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
	switch {
	case err != nil:
		s.keepTrial(r, fmt.Errorf("read the config of the trial's slot: %w", err), false)
		return
	case st == trialEnded:
		// A failure is logged, and the retry is armed.
		_ = s.finishTrial(r)
		return
	case st == trialLost:
		err = s.dropLostTrial(r)
	default:
		log.Printf("config %s was not confirmed by its trial's deadline; rolling back to config %s", r.Version, r.Previous)
		ctx, cancel := s.rollbackContext()
		_, changed, rerr := s.rollbackConfig(ctx, r.Previous)
		cancel()
		if rerr != nil {
			s.keepTrial(r, rerr, changed)
			return
		}
		err = s.endTrial()
	}
	// The deadline that fired is armed no longer, so a record that stays is tried again.
	if err != nil {
		log.Printf("remove the record of the config's trial: %v; trying again in %v", err, s.retryTrial())
		s.armTrial(r, s.retryTrial())
	}
}

// ResumeTrial settles a trial recorded before chalkd started, as settleTrial does: one past its
// deadline rolls back at once, and one whose slot runs the config from before it, as a crash
// before the slot was written leaves it, is finished. A record that cannot be read stays, so
// every change is refused until RollbackConfig removes it. ctx ends when chalkd stops, and so do
// the rollbacks of trials from then on, this one's and those at a deadline. A trial chalkd
// stopped before ResumeTrial took the locks stays as it is for the next start.
//
// chalkd calls it in normal mode once it serves, so a rollback does not keep the node from being
// reached. Until it ends, an upgrade or an image rollback is refused with ABORTED, and the
// requests that take the config lock wait, such as a delivery, a confirmation, GetConfig and
// PullImage; Status does not.
func (s *Server) ResumeTrial(ctx context.Context) {
	// chalkd serves before it calls ResumeTrial, so an upgrade or an image rollback may take
	// s.upgrading first. It refuses itself on the record, or finishes a trial that ended under a
	// context that does not end with chalkd, since s.trialCtx is not set yet.
	s.upgrading.Lock()
	defer s.upgrading.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	s.trialCtx = ctx
	r, open, err := readTrial(s.Paths.StateDir)
	if err != nil {
		log.Printf("read the record of the config's trial: %v; keeping it, so the node takes no change until a rollback removes it", err)
		return
	}
	if open {
		log.Printf("config %s is on trial until %s", r.Version, r.Deadline.UTC().Format(time.RFC3339))
		// A failure is logged, and the retry is armed.
		_, _ = s.settleTrial(r)
	}
}

// RestoreTrialAtBoot ends a trial that was open when the node booted, before chalkos-config applies
// the slot: the slot's previous config, the one from before the trial, becomes its config again,
// and the config on trial its previous one. Every boot ends a trial, before its deadline or after,
// so a reset undoes a config that cut the node off. A trial that was never applied or was rolled
// back leaves the slot as it is, and the record goes. When the slot cannot be read or restored,
// the record stays with the boot's time as its deadline: chalkd then rolls the trial back at once
// as it starts, and a confirmation keeps it only once that rollback failed without changing the
// node. A record that cannot be read stays as well, and the node takes no change until
// RollbackConfig removes it.
func RestoreTrialAtBoot(stateDir, slots string) error {
	r, open, err := readTrial(stateDir)
	if err != nil || !open {
		return err
	}
	dir, st, err := r.state(slots)
	if err != nil {
		return expireAtBoot(stateDir, r, fmt.Errorf("read the config of the trial's slot: %w", err))
	}
	switch st {
	case trialApplied:
		if err := slot.RestorePrevious(dir); err != nil {
			return expireAtBoot(stateDir, r, fmt.Errorf("restore config %s from before the trial of config %s: %w", r.Previous, r.Version, err))
		}
		log.Printf("the trial of config %s was open at boot; restored config %s", r.Version, r.Previous)
	case trialLost:
		log.Printf("the trial of config %s was open at boot, but its slot runs neither it nor config %s", r.Version, r.Previous)
	}
	return removeTrial(stateDir)
}

// expireAtBoot records the boot's time as the deadline of the trial r, which the boot could not
// end for err, so chalkd rolls it back at once as it starts. It returns err, joined with the
// failure to write the record.
func expireAtBoot(stateDir string, r trialRecord, err error) error {
	r.Deadline = time.Now()
	if werr := writeTrial(stateDir, r); werr != nil {
		return errors.Join(err, fmt.Errorf("move the trial's deadline to now: %w", werr))
	}
	return err
}
