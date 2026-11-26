package chalkd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// activationRecord records live delivery, independently of which config the slot stores.
// It contains versions, step names and a plan hash. No documents or tool output go on STATE.
type activationRecord struct {
	Slot             string `json:"slot"`
	Boot             string `json:"boot,omitempty"`
	Attempt          int64  `json:"attempt"`
	Target           string `json:"target"`
	Completed        string `json:"completed,omitempty"`
	CompletedBoot    string `json:"completedBoot,omitempty"`
	State            string `json:"state"`
	Step             string `json:"step,omitempty"`
	FailedStep       string `json:"failedStep,omitempty"`
	Plan             string `json:"plan,omitempty"`
	CompletedBatches int    `json:"completedBatches,omitempty"`
}

const (
	activationRunning  = "in_progress"
	activationFailed   = "failed"
	activationComplete = "complete"
)

func (s *Server) activationPath() string {
	return filepath.Join(s.Paths.StateDir, "activation.json")
}

func (s *Server) bootID() string {
	id, _ := os.ReadFile(s.Paths.BootID)
	return strings.TrimSpace(string(id))
}

func (s *Server) readActivation() (r activationRecord, err error) {
	data, err := os.ReadFile(s.activationPath())
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &r)
	}
	return r, err
}

// liveActivation is the delivery under way, which Status reads without s.mu.
type liveActivation struct {
	// attempt is the live delivery's attempt, zero without one.
	attempt atomic.Int64
	// generation counts the changes of attempt. It only grows, so a read between two that see the
	// same generation saw no delivery start or finish, even one that set attempt back as it was.
	generation atomic.Uint64
	// unwritten is set while the last write of the record failed.
	unwritten atomic.Bool
}

// set makes attempt the live one. The record of a delivery that finishes is written first, so a
// read that sees the new generation sees that record too.
func (l *liveActivation) set(attempt int64) {
	l.generation.Add(1)
	l.attempt.Store(attempt)
}

// snapshot reads the record with read and the live attempt, again while a delivery started or
// finished during the read, so a finished delivery is not taken for an interrupted one.
func (l *liveActivation) snapshot(read func() (activationRecord, error)) (r activationRecord, live int64, err error) {
	for range 3 {
		before := l.generation.Load()
		r, err = read()
		live = l.attempt.Load()
		if err != nil || l.generation.Load() == before {
			return r, live, err
		}
	}
	r, err = read()
	return r, l.attempt.Load(), err
}

func (s *Server) activationStatus(dir, stored, boot string) *nodev1.ConfigActivation {
	r, live, err := s.activating.snapshot(s.readActivation)
	unwritten := s.activating.unwritten.Load()
	switch {
	case err != nil:
		log.Printf("read the config activation record: %v", err)
		return &nodev1.ConfigActivation{State: nodev1.ActivationState_ACTIVATION_STATE_UNKNOWN, Unwritten: unwritten}
	case live != 0 && r.Attempt != live:
		// The record is an earlier delivery's, which the live one could not replace.
		return &nodev1.ConfigActivation{State: nodev1.ActivationState_ACTIVATION_STATE_IN_PROGRESS, Unwritten: unwritten}
	case r.Slot != filepath.Base(dir) || r.Target == "":
		if stored == "" && !unwritten {
			return nil
		}
		return &nodev1.ConfigActivation{State: nodev1.ActivationState_ACTIVATION_STATE_UNRECORDED, Unwritten: unwritten}
	}
	var state nodev1.ActivationState
	switch {
	case r.State == activationRunning && (r.Boot != boot || live != r.Attempt):
		state = nodev1.ActivationState_ACTIVATION_STATE_INTERRUPTED
	case r.State == activationRunning:
		state = nodev1.ActivationState_ACTIVATION_STATE_IN_PROGRESS
	case r.State == activationFailed:
		state = nodev1.ActivationState_ACTIVATION_STATE_FAILED
	case r.State == activationComplete && stored == r.Target:
		state = nodev1.ActivationState_ACTIVATION_STATE_COMPLETE
	case r.State == activationComplete:
		state = nodev1.ActivationState_ACTIVATION_STATE_UNRECORDED
	default:
		state = nodev1.ActivationState_ACTIVATION_STATE_UNKNOWN
	}
	return &nodev1.ConfigActivation{
		TargetVersion: r.Target, CompletedVersion: r.Completed, State: state,
		Step: r.Step, FailedStep: r.FailedStep,
		FromEarlierBoot:        r.Completed != "" && r.CompletedBoot != "" && boot != "" && r.CompletedBoot != boot,
		AttemptFromEarlierBoot: r.Boot != "" && boot != "" && r.Boot != boot,
		Unwritten:              unwritten,
	}
}

// activation tracks one commit while the node's delivery lock is held. The durable snapshot lets
// Status answer independently of that lock and lets a restarted chalkd recognise completed work.
type activation struct {
	node       *Server
	record     activationRecord
	checkpoint bool
}

func (d *delivery) beginActivation() *activation {
	s := d.s
	prior, err := s.readActivation()
	if err != nil {
		log.Printf("read the old config activation record: %v; starting without checkpoints", err)
		prior = activationRecord{}
	}
	if d.keep {
		// A certificate or other payload can change an effect's inputs without changing the
		// config version. An intervening delivery invalidates an interrupted apply's checkpoints.
		if prior.Plan != "" {
			prior.Plan, prior.CompletedBatches = "", 0
			s.saveActivation(prior)
		}
		return nil
	}
	boot := s.bootID()
	r := activationRecord{
		Slot: filepath.Base(d.dir), Boot: boot, Attempt: time.Now().UnixNano(),
		Target: d.stored.Version, State: activationRunning,
	}
	checkpoint := d.resumeEffects && boot != "" && len(d.machine.changes) == 0 &&
		d.msg.FallbackSecret == "" && len(d.msg.Layers) == 0 && len(d.msg.Trust) == 0 &&
		len(d.msg.NodeCertificate) == 0 && len(d.msg.NodeKey) == 0
	if prior.Slot == r.Slot {
		r.Completed, r.CompletedBoot = prior.Completed, prior.CompletedBoot
		if checkpoint && d.fx.baselined && prior.Boot == boot && prior.Target == r.Target &&
			d.replaced == r.Target && (prior.State == activationRunning || prior.State == activationFailed) &&
			prior.CompletedBatches > 0 {
			r.Plan, r.CompletedBatches = prior.Plan, prior.CompletedBatches
		}
	}
	s.activating.set(r.Attempt)
	s.saveActivation(r)
	return &activation{node: s, record: r, checkpoint: checkpoint}
}

func (s *Server) writeActivation(r activationRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.activationPath(), data, 0o600); err != nil {
		return fmt.Errorf("record config activation: %w", err)
	}
	return nil
}

// saveActivation writes r for Status and a later retry. A delivery goes on without it: the
// failure goes to the journal, and the record it would replace is removed, so no later delivery
// resumes from checkpoints that no longer hold.
func (s *Server) saveActivation(r activationRecord) {
	err := s.writeActivation(r)
	s.activating.unwritten.Store(err != nil)
	if err == nil {
		return
	}
	log.Printf("%v; the delivery goes on without it", err)
	if err := os.Remove(s.activationPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("remove the config activation record: %v", err)
	}
}

func (a *activation) step(name string) {
	if a == nil {
		return
	}
	a.record.Step = name
	a.node.saveActivation(a.record)
}

func (a *activation) finish(err error) {
	if a == nil {
		return
	}
	defer a.node.activating.set(0)
	if err != nil {
		a.record.State = activationFailed
		if a.record.FailedStep == "" {
			a.record.FailedStep = a.record.Step
		}
	} else {
		a.record.State = activationComplete
		a.record.Completed, a.record.CompletedBoot = a.record.Target, a.record.Boot
		a.record.Step, a.record.FailedStep = "", ""
		a.record.Plan, a.record.CompletedBatches = "", 0
	}
	a.node.saveActivation(a.record)
}

func (a *activation) plan(due []string, states map[string]outputs.State, ignoreReadiness bool) {
	if a == nil || !a.checkpoint {
		return
	}
	// Go's JSON encoder sorts map keys. Output states include both file and
	// referenced secret hashes. The image slot pins the effect definitions.
	data, err := json.Marshal(struct {
		Due             []string
		Outputs         map[string]outputs.State
		IgnoreReadiness bool
	}{due, states, ignoreReadiness})
	if err != nil {
		log.Printf("the plan of the effects: %v; running every batch", err)
		a.checkpoint = false
		return
	}
	key := fmt.Sprintf("%x", sha256.Sum256(data))
	if a.record.Plan != key {
		a.record.Plan, a.record.CompletedBatches = key, 0
		a.node.saveActivation(a.record)
	}
}

// hooks resume only a contiguous prefix of batches that did declarative unit work. A probe,
// rule, command, or layer action can depend on live state and must run again.
func (a *activation) hooks(ctx context.Context, defs map[string]effects.Definition) *effects.BatchProgress {
	if a == nil {
		return nil
	}
	reusable := func(batch effects.Batch) bool {
		if !a.checkpoint {
			return false
		}
		for _, name := range batch.Effects {
			d := defs[name]
			if d.Run != "" || d.Chalkd != "" || d.When != "" || d.WaitFor != nil {
				return false
			}
			for _, units := range [][]string{d.Reload, d.Restart} {
				for _, unit := range units {
					state, err := systemctl.ActiveState(ctx, a.node.Run, unit)
					if err != nil || state != "active" {
						return false
					}
				}
			}
		}
		return true
	}
	return &effects.BatchProgress{
		Before: func(batch effects.Batch) bool {
			skip := batch.Index < a.record.CompletedBatches && reusable(batch)
			if !skip && batch.Index < a.record.CompletedBatches {
				// A replayed prerequisite can invalidate every later checkpoint.
				a.record.CompletedBatches = batch.Index
			}
			a.step(fmt.Sprintf("effect batch %d (%s)", batch.Index+1, strings.Join(batch.Effects, ", ")))
			return skip
		},
		After: func(batch effects.Batch, succeeded bool) {
			if !succeeded {
				if a.record.FailedStep == "" {
					a.record.FailedStep = a.record.Step
				}
				a.node.saveActivation(a.record)
				return
			}
			if batch.Index == a.record.CompletedBatches && reusable(batch) {
				a.record.CompletedBatches++
				a.node.saveActivation(a.record)
			}
		},
	}
}
