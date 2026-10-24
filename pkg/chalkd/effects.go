package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// pendingEffects are the running image's effects, and what a delivery compares their rules with: the
// config and the facts as they were before it changed anything, or before the first delivery
// since one was last applied whole, as its baseline holds them.
type pendingEffects struct {
	defs  map[string]effects.Definition
	rules []*rules.EffectRule
	// node is the node's name, which the rules read as node.name.
	node      string
	next, old []config.Document
	// oldHashed tells that the secret values of old, a baseline's, are hashed already.
	oldHashed bool
	// recorded tells that effectsOf wrote the baseline for this delivery, which no earlier one left.
	recorded bool
	before   rules.Facts
	// outputs are the states of the outputs before the delivery, by name.
	outputs map[string]outputs.State
	// kinds are the hashes of the kinds' documents before the delivery, by kind, and schemas the
	// schemas that tell their secretRefs.
	kinds   map[string]string
	schemas *schema.Schemas
}

// effectsOf compiles the rules of the running image's effects for a delivery to the node called
// name that makes next, checked against schemas, the config of the running slot in place of old,
// and loads every fact they read: the caller calls it before the delivery changes anything. A
// delivery without config keeps old. The image's build compiled the rules already, so one that
// does not compile is the node's own failure.
// The rules compare with the baseline instead while there is one, as a delivery that failed part
// way left it; otherwise effectsOf records old and the facts, and the outputs' states and the
// kinds' hashes, as the baseline, which consume removes once the delivery is applied whole, and
// refused once it is refused before it changed anything. A retry, or another delivery, then takes
// the effects of what the failed delivery changed already too.
func (s *Server) effectsOf(ctx context.Context, name string, next, old []config.Document, schemas *schema.Schemas, secret string) (*pendingEffects, error) {
	defs, err := effects.Read(s.Loader.Effects)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	// A delivery without config checks no document against schemas, so schemas is nil; the kinds'
	// hashes, here and in due, need the running image's own then, the way its outputs already load
	// them for a nil schemas when there are outputs and documents to render. A delivery of neither
	// config nor documents, such as a node's first certificate, hashes no kind and needs none.
	if schemas == nil && (len(old) > 0 || len(next) > 0) {
		if schemas, err = schema.LoadSchemas(s.Loader.Schemas); err != nil {
			return nil, failed(connect.CodeInternal, "the running image's schemas: %v", err)
		}
	}
	e := &pendingEffects{defs: defs, node: name, next: next, old: old, schemas: schemas}
	var errs []error
	for _, effect := range slices.Sorted(maps.Keys(defs)) {
		// An effect without a rule fires on its outputs or kinds alone.
		if defs[effect].When == "" {
			continue
		}
		p, err := rules.CompileEffect(effect, defs[effect].When)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		e.rules = append(e.rules, p)
	}
	if len(errs) > 0 {
		return nil, failed(connect.CodeInternal, "the running image's effects: %v", errors.Join(errs...))
	}
	b, found, err := readBaseline(s.Paths.EffectsBaseline)
	if err != nil {
		// Replaced below: the next delivery compares with the state as it is now.
		log.Printf("the baseline of the effects: %v; comparing with the running slot", err)
	}
	if found {
		e.old, e.oldHashed, e.before, e.outputs, e.kinds = b.documents(), true, b, b.Outputs, b.Kinds
		return e, nil
	}
	if e.outputs, err = s.Loader.OutputStates(old, schemas); err != nil {
		return nil, failed(connect.CodeInternal, "the outputs the node serves: %v", err)
	}
	if e.kinds, err = config.KindHashes(old, schemas); err != nil {
		return nil, failed(connect.CodeInternal, "the kinds the node runs: %v", err)
	}
	before, err := s.factsFor(ctx, next, old, secret)
	if err != nil {
		return nil, err
	}
	e.before = before
	// Every fact the rules read loads now, before anything changes. One that cannot be loaded keeps
	// its error, which makes the rules that read it due.
	if err := newBaseline(old, e.factNames(), before, e.outputs, e.kinds).write(s.Paths.EffectsBaseline); err != nil {
		return nil, failed(connect.CodeInternal, "record the config, facts, outputs and kinds the effects compare with: %v", err)
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

// oldConfig is the old config as rules see it.
func (e *pendingEffects) oldConfig() map[string]any {
	if e.oldHashed {
		return rules.HashedConfigOf(e.old)
	}
	return rules.ConfigOf(e.old)
}

// due returns the names of the effects due once the delivery is applied, sorted: those of whose
// outputs one has a state in states, the outputs swapped in, other than before, those one of
// whose kinds' documents, or a Secret they reference, changed, and those whose rule holds,
// loading the facts as they are now. A rule that cannot be evaluated counts as true, and goes to
// the journal and to warnings, which say why without the values it read.
func (s *Server) due(ctx context.Context, e *pendingEffects, states map[string]outputs.State, secret string) (due, warnings []string, err error) {
	after, err := s.factsFor(ctx, e.next, nil, secret)
	if err != nil {
		return nil, nil, err
	}
	// Command facts read the old config as the rules do.
	after.Input = map[string]any{"config": rules.ConfigOf(e.next), "oldConfig": e.oldConfig()}
	ruled, errs := rules.Due(e.rules, rules.EffectInput{
		Documents: e.next, Old: e.old, OldHashed: e.oldHashed, Node: e.node, Facts: after, OldFacts: e.before,
	})
	for _, err := range errs {
		w := fmt.Sprintf("the rule of %v; taking the effect all the same", err)
		log.Print(w)
		warnings = append(warnings, w)
	}
	for _, r := range ruled {
		due = append(due, r.Name)
	}
	kinds, err := config.KindHashes(e.next, e.schemas)
	if err != nil {
		return nil, nil, failed(connect.CodeInternal, "the kinds of the delivery: %v", err)
	}
	for name, d := range e.defs {
		changedOutput := slices.ContainsFunc(d.Outputs, func(o string) bool { st, ok := states[o]; return ok && st != e.outputs[o] })
		changedKind := slices.ContainsFunc(d.Kinds, func(k string) bool { return kinds[k] != e.kinds[k] })
		if changedOutput || changedKind {
			due = append(due, name)
		}
	}
	slices.Sort(due)
	return slices.Compact(due), warnings, nil
}

// consume takes the effects a delivery asks for once it is applied. With stored set the delivery
// wrote the running slot, whose documents it writes for the granted units first; so does a
// delivery that compares with a baseline, since the one that failed part way may have written the
// slot and not its documents, which the units would restart on otherwise. The staged outputs
// follow, swapped in before any effect is taken. It returns the restarted units and the warnings of rules that could not be evaluated. Once every effect is
// taken it removes the baseline; until then a retry compares with it and takes them again.
func (s *Server) consume(ctx context.Context, e *pendingEffects, staged *outputs.Staged, stored bool, secret string) ([]string, []string, error) {
	states := staged.States()
	due, warnings, err := s.due(ctx, e, states, secret)
	if err != nil {
		return nil, nil, err
	}
	if stored || e.oldHashed {
		if err := s.Loader.LoadConfig(); err != nil {
			// What chalkd's loops read, the slot, the cluster file and the share, is in place.
			a := actionsOf(e.defs, due)
			s.actOnSelf(a.chalkd == effects.ChalkdReload, a.chalkd == effects.ChalkdRestart)
			return nil, nil, failed(connect.CodeInternal, "write the config for the granted units: %v%s", err, a.byHand())
		}
	}
	// The outputs, checked before the delivery changed anything, follow the documents.
	if err := staged.Swap(); err != nil {
		a := actionsOf(e.defs, due)
		s.actOnSelf(a.chalkd == effects.ChalkdReload, a.chalkd == effects.ChalkdRestart)
		return nil, nil, failed(connect.CodeInternal, "%v%s", err, a.byHand())
	}
	restarted, err := s.applyEffects(ctx, actionsOf(e.defs, due))
	if err != nil {
		return nil, nil, err
	}
	s.dropBaseline()
	return restarted, warnings, nil
}

// refused removes the baseline of a delivery refused before it changed anything, which leaves the
// next one nothing to take again. A baseline an earlier delivery that failed part way left stays.
func (s *Server) refused(e *pendingEffects) {
	if e.recorded {
		s.dropBaseline()
	}
}

// failedPartWay acts on the effects on chalkd's own loops for a delivery that failed once it
// changed the node and before its slot was written: the files its loops read besides the slot,
// the cluster file and the share, may be written by then, and the rules compare them as they are.
// The baseline stays, so a retry takes every effect of the delivery.
func (s *Server) failedPartWay(ctx context.Context, e *pendingEffects, secret string) {
	due, _, err := s.due(ctx, e, nil, secret)
	if err != nil {
		log.Printf("the effects on chalkd's loops: %v", err)
		return
	}
	a := actionsOf(e.defs, due)
	s.actOnSelf(a.chalkd == effects.ChalkdReload, a.chalkd == effects.ChalkdRestart)
}

// actions are what the due effects ask for together: the units to reload and to restart, each
// sorted, those of restart to start too, the commands to run, in the order of their effects'
// names, and what chalkd does to its own loops, a restart taking precedence over a reload.
type actions struct {
	reload, restart []string
	start           map[string]bool
	run             []string
	chalkd          effects.ChalkdAction
}

func actionsOf(defs map[string]effects.Definition, due []string) actions {
	a := actions{start: map[string]bool{}}
	for _, name := range due {
		d := defs[name]
		a.reload = append(a.reload, d.Reload...)
		a.restart = append(a.restart, d.Restart...)
		for _, unit := range d.Restart {
			a.start[unit] = a.start[unit] || d.Start
		}
		if d.Run != "" && !slices.Contains(a.run, d.Run) {
			a.run = append(a.run, d.Run)
		}
		switch {
		case d.Chalkd == effects.ChalkdRestart:
			a.chalkd = d.Chalkd
		case d.Chalkd == effects.ChalkdReload && a.chalkd == "":
			a.chalkd = d.Chalkd
		}
	}
	slices.Sort(a.reload)
	slices.Sort(a.restart)
	a.reload, a.restart = slices.Compact(a.reload), slices.Compact(a.restart)
	return a
}

// applyEffects reloads the units of a.reload that run, runs a.run, then restarts the units of
// a.restart, and returns the restarted units; chalkd acts on its own loops last. A unit it
// restarts is not reloaded: the restart reads all a reload would.
// The restarts take one systemctl call each way: try-restart for a unit that runs, or ran and
// stays active, so a unit that never started, or was stopped, stays so; restart for a unit that
// starts too; and for a unit that failed, such as one that found no document of its kind at boot,
// reset-failed and restart, since the new config may be what it lacked and try-restart leaves a
// failed unit alone.
func (s *Server) applyEffects(ctx context.Context, a actions) ([]string, error) {
	// Once the units it restarts read the new config, and whether or not they could: the
	// cluster and the share the loops read are in place.
	defer s.actOnSelf(a.chalkd == effects.ChalkdReload, a.chalkd == effects.ChalkdRestart)
	states := map[string]string{}
	for _, unit := range append(slices.Clone(a.reload), a.restart...) {
		if _, ok := states[unit]; ok {
			continue
		}
		out, err := s.Run.Run(ctx, "systemctl", "show", "--property=ActiveState", "--value", unit)
		if err != nil {
			log.Printf("the state of %s: %v; acting on it as if it ran", unit, err)
		}
		states[unit] = strings.TrimSpace(string(out))
	}
	var errs []error
	var reload []string
	for _, unit := range a.reload {
		// A unit that does not run reads its config when it starts.
		if slices.Contains(a.restart, unit) || states[unit] == "inactive" || states[unit] == "failed" {
			continue
		}
		reload = append(reload, unit)
	}
	byHand := actions{}
	if len(reload) > 0 {
		if _, err := s.Run.Run(ctx, "systemctl", append([]string{"reload"}, reload...)...); err != nil {
			byHand.reload = reload
			errs = append(errs, fmt.Errorf("reload %s: %w", strings.Join(reload, " "), err))
		}
	}
	for _, command := range a.run {
		argv := strings.Fields(command)
		if _, err := s.Run.Run(ctx, argv[0], argv[1:]...); err != nil {
			byHand.run = append(byHand.run, command)
			errs = append(errs, fmt.Errorf("run %s: %w", command, err))
		}
	}
	var failedUnits, start, try []string
	for _, unit := range a.restart {
		switch {
		case states[unit] == "failed":
			failedUnits = append(failedUnits, unit)
		case a.start[unit]:
			start = append(start, unit)
		default:
			try = append(try, unit)
		}
	}
	var restarted []string
	run := func(verb string, units []string) {
		if len(units) == 0 {
			return
		}
		if _, err := s.Run.Run(ctx, "systemctl", append([]string{verb}, units...)...); err != nil {
			byHand.restart = append(byHand.restart, units...)
			errs = append(errs, fmt.Errorf("%s %s: %w", verb, strings.Join(units, " "), err))
			return
		}
		restarted = append(restarted, units...)
	}
	if len(failedUnits) > 0 {
		// The failures that put them in their state count against their start limit, which would
		// refuse the restart.
		if _, err := s.Run.Run(ctx, "systemctl", append([]string{"reset-failed"}, failedUnits...)...); err != nil {
			byHand.restart = append(byHand.restart, failedUnits...)
			errs = append(errs, fmt.Errorf("reset-failed %s: %w", strings.Join(failedUnits, " "), err))
			failedUnits = nil
		}
	}
	start = append(failedUnits, start...)
	slices.Sort(start)
	run("restart", start)
	run("try-restart", try)
	if len(errs) > 0 {
		slices.Sort(byHand.restart)
		hand := byHand.byHand()
		// The Kubernetes units start only once their preparation succeeds, whose reason tells
		// more than systemctl's error; another unit's failure has its own.
		if slices.ContainsFunc(byHand.restart, isKubernetesUnit) && s.Kubernetes != nil {
			if problem, err := preparation(s.Kubernetes.Paths); err == nil && problem != "" && problem != preparing {
				return nil, failed(connect.CodeFailedPrecondition, "%v; the node runs no kubelet: %s%s", errors.Join(errs...), problem, hand)
			}
		}
		return nil, failed(connect.CodeInternal, "%v%s", errors.Join(errs...), hand)
	}
	slices.Sort(restarted)
	return restarted, nil
}

// isKubernetesUnit tells whether unit is one of the units the node's Kubernetes preparation
// starts: the preparation itself, and the kubelet that waits for it.
func isKubernetesUnit(unit string) bool {
	return unit == "chalkos-kubernetes.service" || unit == "kubelet.service"
}

// actOnSelf reloads or restarts chalkd's Kubernetes loops, on a node with Kubernetes; a restart
// starts the control plane's loop again too.
func (s *Server) actOnSelf(reload, restart bool) {
	if s.actedOnSelf != nil {
		s.actedOnSelf(reload, restart)
	}
	if s.Kubernetes == nil {
		return
	}
	switch {
	case restart:
		s.Kubernetes.Restart()
	case reload:
		s.Kubernetes.Reload()
	}
}

// byHand tells the operator what the delivery still has to do to the units.
func (a actions) byHand() string {
	var todo []string
	if len(a.reload) > 0 {
		todo = append(todo, "reload "+strings.Join(a.reload, " "))
	}
	for _, command := range a.run {
		todo = append(todo, "run "+command)
	}
	if len(a.restart) > 0 {
		todo = append(todo, "restart "+strings.Join(a.restart, " "))
	}
	if len(todo) == 0 {
		return ""
	}
	return "; the delivery is applied, so " + strings.Join(todo, " and ") + " by hand once that is fixed, or deliver it again"
}
