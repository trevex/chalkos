package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// pendingEffects are the running image's effects, and what a delivery compares their rules with:
// the config and the facts as they were before it changed anything, or before the first delivery
// since one was last applied whole, as its baseline holds them.
type pendingEffects struct {
	defs  map[string]effects.Definition
	rules []*rules.EffectRule
	// node is the node's name, which the rules read as node.name.
	node      string
	next, old config.Redacted
	// baselined tells that old and what goes with it are a baseline's.
	baselined bool
	// recorded tells that effectsOf wrote the baseline for this delivery, which no earlier one left.
	recorded bool
	before   rules.Facts
	// outputs are the states of the outputs before the delivery, by name.
	outputs map[string]outputs.State
	// kinds are the hashes of the kinds' documents before the delivery, by kind, and schemas the
	// schemas that tell their secretRefs.
	kinds   map[string]string
	schemas *schema.Schemas
	// attempts are what the deliveries that failed part way since the baseline was recorded
	// applied, which the rules, outputs and kinds compare with too.
	attempts []baselineAttempt
	// attempt is what this delivery applies, as recordAttempt added it to the baseline; nil until
	// then. withoutAttempt is the baseline as it was before, which refused restores.
	attempt        *baselineAttempt
	withoutAttempt []byte
}

// effectsOf takes the running image's effects and loads every fact their rules read, before the
// delivery to the node called name changes anything. next replaces old as the running slot's
// config (a delivery without config keeps old); schemas are the running image's, nil only without
// documents to read. A rule that does not compile is the node's own failure: the build compiled
// them. While a baseline exists, as a delivery that failed part way leaves one, the
// rules compare with it and with each failed attempt since; otherwise old, the facts, the outputs'
// states and the kinds' hashes are recorded as the baseline, unless dry. consume removes the
// baseline once a delivery is applied whole, and refused once one is refused before it changed
// anything; until then every delivery takes the effects of what the failed one changed too.
func (s *Server) effectsOf(ctx context.Context, name string, next, old config.Redacted, schemas *schema.Schemas, secret string, dry bool) (*pendingEffects, error) {
	c := s.Loader.Contract
	defs := c.Effects
	// An action no active plugin has would fail only once the delivery changed the node.
	for _, effect := range slices.Sorted(maps.Keys(defs)) {
		if a := defs[effect].Chalkd; a != "" {
			if _, err := s.Plugins.Action(a); err != nil {
				return nil, fmt.Errorf("%s: effect %s: %w", filepath.Join(c.Dir, contract.EffectsFile), effect, err)
			}
		}
	}
	compiled, err := c.EffectRules()
	if err != nil {
		return nil, fmt.Errorf("the running image's effects: %w", err)
	}
	e := &pendingEffects{defs: defs, rules: compiled, node: name, next: next, old: old, schemas: schemas}
	b, found, err := readBaseline(s.Paths.EffectsBaseline)
	if err != nil {
		// Replaced below: the next delivery compares with the state as it is now.
		log.Printf("the baseline of the effects: %v; comparing with the running slot", err)
	}
	if found {
		e.old, e.baselined, e.before, e.outputs, e.kinds, e.attempts = b.documents(), true, b.Facts, b.Outputs, b.Kinds, b.Attempts
		return e, nil
	}
	if e.outputs, err = s.Loader.OutputStates(e.old, schemas); err != nil {
		return nil, fmt.Errorf("the outputs the node serves: %w", err)
	}
	if e.kinds, err = config.KindHashes(e.old, schemas); err != nil {
		return nil, fmt.Errorf("the kinds the node runs: %w", err)
	}
	before := s.factsFor(ctx, e.next, e.old, secret)
	e.before = before
	// A dry run compares with the state as it is now and records nothing.
	if dry {
		return e, nil
	}
	// Every fact the rules read loads now, before anything changes. One that cannot be loaded keeps
	// its error, which makes the rules that read it due.
	if err := newBaseline(e.old, e.factNames(), before, e.outputs, e.kinds).write(s.Paths.EffectsBaseline); err != nil {
		return nil, fmt.Errorf("record the config, facts, outputs and kinds the effects compare with: %w", err)
	}
	e.recorded = true
	return e, nil
}

// factNames are the facts the rules read, sorted.
func (e *pendingEffects) factNames() []string {
	var names []string
	for _, r := range e.rules {
		for _, n := range r.Facts {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	return names
}

// due returns the sorted names of the effects due once the delivery is applied: compared with
// before it, or with an attempt that failed since, one of the effect's outputs in states differs,
// a document of one of its kinds or a Secret they reference differs, or its rule holds on the
// facts as they are now. A rule that cannot be evaluated counts as true; its error goes to the
// journal and to warnings without the values it read. The facts as they are now go to the
// delivery's attempt in the baseline.
func (s *Server) due(ctx context.Context, e *pendingEffects, states map[string]outputs.State, secret string) (due, warnings []string, err error) {
	o, warnings, err := s.outcome(ctx, e, states, secret)
	for _, f := range o.Effects {
		due = append(due, f.Name)
	}
	return due, warnings, err
}

// outcome is what due finds, with the outputs that changed and what fired each effect, as a dry
// run's plan names them.
func (s *Server) outcome(ctx context.Context, e *pendingEffects, states map[string]outputs.State, secret string) (check.Outcome, []string, error) {
	after := s.factsFor(ctx, e.next, config.Redacted{}, secret)
	// Command facts read the old config as the rules do.
	after.Input = rules.FactInput(e.next, e.old)
	if e.attempt != nil {
		// The delivery changed the node by now, so the facts are those its units read.
		if err := setAttemptFacts(s.Paths.EffectsBaseline, *e.attempt, recordFacts(e.factNames(), after)); err != nil {
			// Without them every rule that reads a fact compares as due with this attempt.
			log.Printf("record the facts of the delivery in the baseline of the effects: %v", err)
		}
	}
	o, warnings, err := check.Due(e.checkEffects(), check.Delivery{Node: e.node, Documents: e.next, Schemas: e.schemas, Outputs: states}, e.comparisons(after))
	if err != nil {
		return check.Outcome{}, nil, err
	}
	for _, w := range warnings {
		log.Print(w)
	}
	return o, warnings, nil
}

// checkEffects are the effects as check.Due and check.PlanOf read them.
func (e *pendingEffects) checkEffects() check.Effects {
	return check.Effects{Defs: e.defs, Rules: e.rules}
}

// comparisons are the states due compares a delivery with: the config and the facts from before
// it, and what each delivery that failed since the baseline applied, all with after, the facts as
// they are now.
func (e *pendingEffects) comparisons(after *facts.Loader) []check.Comparison {
	olds := []check.Comparison{{Documents: e.old, Facts: after, OldFacts: e.before, Outputs: e.outputs, Kinds: e.kinds}}
	for _, a := range e.attempts {
		docs := a.documents()
		// Command facts read the attempt's config as the old one.
		facts := after.For(rules.FactInput(e.next, docs))
		olds = append(olds, check.Comparison{Documents: docs, Facts: facts, OldFacts: a.Facts, Outputs: a.Outputs, Kinds: a.Kinds})
	}
	return olds
}

// recordAttempt adds what the delivery of e applies to the baseline before it changes anything:
// its documents, the states of the outputs it rendered and its kinds' hashes; due adds the facts
// later. Should it fail or crash part way, a unit it restarted runs its config, so the next
// delivery, even of the config from before it, compares with it too. The attempts go with the
// baseline once a delivery is applied whole.
func (s *Server) recordAttempt(e *pendingEffects, states map[string]outputs.State) error {
	kinds, err := config.KindHashes(e.next, e.schemas)
	if err != nil {
		// Without them every effect on a kind the delivery holds is due.
		log.Printf("the kinds of the delivery: %v", err)
	}
	a := baselineAttempt{Documents: e.next.Objects(), Outputs: states, Kinds: kinds}
	previous, err := addAttempt(s.Paths.EffectsBaseline, a)
	if err != nil {
		return fmt.Errorf("record the delivery in the baseline of the effects: %w", err)
	}
	e.attempt, e.withoutAttempt = &a, previous
	return nil
}

// effectsTimeout bounds what a delivery does once it wrote the slot, which the client cannot
// cancel.
const effectsTimeout = 10 * time.Minute

// deliveryContext is the context a delivery runs in from the write of its slot on. The node runs
// the delivered config by then, and a layer action left out would leave chalkd's loops on the
// config from before, so the client going away cancels nothing. lifetime, which ends when chalkd
// stops, does, and so does the earliest of ctx's deadline, until when set, as a trial's deadline,
// and effectsTimeout from now.
func deliveryContext(ctx, lifetime context.Context, until time.Time) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(effectsTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if !until.IsZero() && until.Before(deadline) {
		deadline = until
	}
	work, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	stop := context.AfterFunc(lifetime, cancel)
	return work, func() {
		stop()
		cancel()
	}
}

// lifetime ends when chalkd stops: the context ResumeTrial gives, and Background before it.
func (s *Server) lifetime() context.Context {
	if s.trial.ctx != nil {
		return s.trial.ctx
	}
	return context.Background()
}

// consume takes the effects of an applied delivery, in this order: it writes the running slot's
// documents for the granted units when stored says the delivery wrote the slot, or when it
// compares with a baseline (a failed delivery may have written the slot and not its documents),
// swaps in the staged outputs, and takes the effects. It returns the restarted units and the
// warnings of rules that could not be evaluated, and removes the baseline once every effect is
// taken; until then a retry compares with it and takes them again.
func (s *Server) consume(ctx context.Context, e *pendingEffects, staged *outputs.Staged, activation *activation, stored bool, secret string, ignoreReadiness bool) ([]string, []string, error) {
	activation.step("plan runtime effects")
	states := staged.States()
	due, warnings, err := s.due(ctx, e, states, secret)
	if err != nil {
		return nil, nil, err
	}
	schedule, err := effects.PlanSchedule(e.defs, due)
	if err != nil {
		return nil, nil, err
	}
	activation.plan(due, states, ignoreReadiness)
	if stored || e.baselined {
		activation.step("publish unit config")
		if err := s.Loader.LoadConfig(); err != nil {
			// What the layer actions read, the slot and the layers' files, is in place.
			a := effects.ActionsOf(e.defs, due)
			s.actOnSelf(a.Chalkd)
			return nil, nil, fmt.Errorf("write the config for the granted units: %w%s", err, a.ByHand())
		}
	}
	// The outputs, checked before the delivery changed anything, follow the documents.
	activation.step("publish rendered outputs")
	if err := staged.Swap(); err != nil {
		a := effects.ActionsOf(e.defs, due)
		s.actOnSelf(a.Chalkd)
		return nil, nil, fmt.Errorf("%w%s", err, a.ByHand())
	}
	var warnReadiness func(error)
	if ignoreReadiness {
		warnReadiness = func(err error) {
			warning := fmt.Sprintf("ignored a failed runtime readiness check: %v", err)
			log.Print(warning)
			warnings = append(warnings, warning)
		}
	}
	// The time the effects have, for the error of effects cut short.
	var bound time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		bound = time.Until(deadline).Round(time.Second)
	}
	ran := map[string]bool{}
	act := func(ctx context.Context, name string) error {
		ran[name] = true
		return s.runLayerAction(ctx, name)
	}
	restarted, err := schedule.Apply(ctx, s.Run, e.defs, act, warnReadiness, activation.hooks(ctx, e.defs))
	if err != nil {
		// The slot and the layers' files are in place, so chalkd's loops read them also when a
		// batch failed or the effects were cut short.
		var rest []string
		for _, name := range effects.ActionsOf(e.defs, due).Chalkd {
			if !ran[name] {
				rest = append(rest, name)
			}
		}
		s.actOnSelf(rest)
	}
	switch ctxErr := ctx.Err(); {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		err = fmt.Errorf("effects did not finish within %v: %w", bound, err)
	case ctxErr != nil:
		err = fmt.Errorf("chalkd stopped before the effects finished: %w", err)
	}
	if ae, ok := errors.AsType[*effects.ApplyError](err); ok && ae.Why != "" {
		err = layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
	}
	if err != nil {
		return nil, nil, err
	}
	s.dropBaseline()
	return restarted, warnings, nil
}

// refused removes the baseline of a delivery refused before it changed anything, which leaves the
// next one nothing to take again. A baseline an earlier delivery that failed part way left stays,
// as it was before the delivery recorded its attempt.
func (s *Server) refused(e *pendingEffects) {
	switch {
	case e.recorded:
		s.dropBaseline()
	case e.withoutAttempt != nil:
		if err := atomicfile.WriteFile(s.Paths.EffectsBaseline, e.withoutAttempt, 0o600); err != nil {
			// The attempt stays, without facts, which makes every rule that reads a fact due.
			log.Printf("remove the refused delivery from the baseline of the effects: %v", err)
		}
	}
}

// failedPartWay runs the layer actions of the effects for a delivery that failed once it changed
// the node and before its slot was written: the files they read besides the slot, the layers'
// files, may be written by then, and the rules compare them as they are.
// The baseline stays, so a retry takes every effect of the delivery.
func (s *Server) failedPartWay(ctx context.Context, e *pendingEffects, secret string) {
	due, _, err := s.due(ctx, e, nil, secret)
	if err != nil {
		log.Printf("the effects on chalkd's loops: %v", err)
		return
	}
	a := effects.ActionsOf(e.defs, due)
	s.actOnSelf(a.Chalkd)
}

// actOnSelf runs the layer actions of a delivery that failed before its effects, on what only the
// running chalkd holds, and logs their failures.
func (s *Server) actOnSelf(names []string) {
	for _, name := range names {
		if err := s.runLayerAction(context.Background(), name); err != nil {
			log.Printf("layer action %s: %v", name, err)
		}
	}
}

func (s *Server) runLayerAction(ctx context.Context, name string) error {
	if s.actedOnSelf != nil {
		s.actedOnSelf(name)
	}
	run, err := s.Plugins.Action(name)
	if err != nil {
		return err
	}
	return run(ctx)
}
