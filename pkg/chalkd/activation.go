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

func (s *Server) activationStatus(dir, stored, boot string) *nodev1.ConfigActivation {
	r, err := s.readActivation()
	if err != nil {
		log.Printf("read the config activation record: %v", err)
		return &nodev1.ConfigActivation{State: "unknown"}
	}
	if r.Slot != filepath.Base(dir) || r.Target == "" {
		if stored == "" {
			return nil
		}
		return &nodev1.ConfigActivation{State: "unrecorded"}
	}
	state := r.State
	if state == activationRunning && (r.Boot != boot || s.activating.Load() != r.Attempt) {
		state = "interrupted"
	}
	if state == activationComplete && stored != r.Target {
		state = "unrecorded"
	}
	return &nodev1.ConfigActivation{
		TargetVersion: r.Target, CompletedVersion: r.Completed, State: state,
		Step: r.Step, FailedStep: r.FailedStep,
		FromEarlierBoot:        r.Completed != "" && r.CompletedBoot != "" && boot != "" && r.CompletedBoot != boot,
		AttemptFromEarlierBoot: r.Boot != "" && boot != "" && r.Boot != boot,
	}
}

// activation tracks one commit while the node's delivery lock is held. The durable snapshot lets
// Status answer independently of that lock and lets a restarted chalkd recognize completed work.
type activation struct {
	node       *Server
	record     activationRecord
	checkpoint bool
}

func (d *delivery) beginActivation() (*activation, error) {
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
			if err := s.writeActivation(prior); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	boot := s.bootID()
	r := activationRecord{
		Slot: filepath.Base(d.dir), Boot: boot, Attempt: time.Now().UnixNano(),
		Target: d.stored.Version, State: activationRunning, Step: "record effects baseline",
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
	s.activating.Store(r.Attempt)
	if err := s.writeActivation(r); err != nil {
		s.activating.Store(0)
		return nil, err
	}
	return &activation{node: s, record: r, checkpoint: checkpoint}, nil
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

func (a *activation) step(name string) error {
	if a == nil {
		return nil
	}
	a.record.Step = name
	return a.node.writeActivation(a.record)
}

func (a *activation) finish(err error) error {
	if a == nil {
		return nil
	}
	defer a.node.activating.Store(0)
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
	return a.node.writeActivation(a.record)
}

func (a *activation) plan(due []string, states map[string]outputs.State, force bool) error {
	if a == nil || !a.checkpoint {
		return nil
	}
	// Go's JSON encoder sorts map keys. Output states include both file and
	// referenced secret hashes. The image slot pins the effect definitions.
	data, err := json.Marshal(struct {
		Due     []string
		Outputs map[string]outputs.State
		Force   bool
	}{due, states, force})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%x", sha256.Sum256(data))
	if a.record.Plan != key {
		a.record.Plan, a.record.CompletedBatches = key, 0
		return a.node.writeActivation(a.record)
	}
	return nil
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
		Before: func(batch effects.Batch) (bool, error) {
			step := fmt.Sprintf("effect batch %d (%s)", batch.Index+1, strings.Join(batch.Effects, ", "))
			if batch.Index < a.record.CompletedBatches {
				if reusable(batch) {
					return true, a.step(step)
				}
				// A replayed prerequisite can invalidate every later checkpoint.
				a.record.CompletedBatches = batch.Index
			}
			return false, a.step(step)
		},
		After: func(batch effects.Batch, succeeded bool) error {
			if !succeeded {
				if a.record.FailedStep == "" {
					a.record.FailedStep = a.record.Step
				}
				return a.node.writeActivation(a.record)
			}
			if batch.Index == a.record.CompletedBatches && reusable(batch) {
				a.record.CompletedBatches++
				return a.node.writeActivation(a.record)
			}
			return nil
		},
	}
}
