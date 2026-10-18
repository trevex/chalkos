package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// selfUnit is the consumer that is chalkd itself, which acts on its rules in-process: a reload
// starts the control plane's loop again, which reads the share and applies the control plane's
// objects, and a restart every Kubernetes loop, which read the cluster when they start.
const selfUnit = "chalkd"

// effects are the restart and reload rules of the running image's consumers, and what a delivery
// compares them with: the kinds it changes and the facts as they were before it changed anything,
// or before the first delivery since one was last applied whole, as its baseline holds them.
type effects struct {
	consumers map[string]nodeconfig.Consumer
	rules     []*rules.Effect
	// node is the node's name, which the rules read as node.name.
	node      string
	next, old []config.Document
	// oldHashed tells that the secret values of old, a baseline's, are hashed already.
	oldHashed bool
	// recorded tells that effectsOf wrote the baseline for this delivery, which no earlier one left.
	recorded bool
	changed  []string
	before   rules.Facts
}

// effectsOf compiles the rules of the running image's consumers for a delivery to the node called
// name that makes next, checked against schemas, the config of the running slot in place of old,
// and loads every fact they read: the caller calls it before the delivery changes anything. A
// delivery without config keeps old, which changes no kind. The image's build compiled the rules
// already, so one that does not compile is the node's own failure.
// The rules compare with the baseline instead while there is one, as a delivery that failed part
// way left it; otherwise effectsOf records old and the facts as the baseline, which consume
// removes once the delivery is applied whole, and refused once it is refused before it changed
// anything. A retry, or another delivery, then takes the effects of what the failed delivery
// changed already too.
func (s *Server) effectsOf(ctx context.Context, name string, next, old []config.Document, schemas *schema.Schemas, secret string) (*effects, error) {
	consumers, err := nodeconfig.ReadConsumers(s.Loader.Consumers)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	e := &effects{consumers: consumers, node: name, next: next, old: old}
	var errs []error
	for _, unit := range sortedNames(consumers) {
		c := consumers[unit]
		for _, r := range []struct{ action, when string }{{rules.Reload, c.Reload.When}, {rules.Restart, c.Restart.When}} {
			// A consumer without a rule for an action never takes it.
			if r.when == "" {
				continue
			}
			p, err := rules.CompileEffect(unit, r.action, r.when)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			e.rules = append(e.rules, p)
		}
	}
	if len(errs) > 0 {
		return nil, failed(connect.CodeInternal, "the running image's consumers: %v", errors.Join(errs...))
	}
	b, found, err := readBaseline(s.Paths.EffectsBaseline)
	if err != nil {
		// Replaced below: the next delivery compares with the state as it is now.
		log.Printf("the baseline of the consumers' rules: %v; comparing with the running slot", err)
	}
	if found {
		e.old, e.oldHashed, e.before = b.documents(), true, b
		if schemas == nil {
			// A delivery without config still compares the running slot's with the baseline's.
			if schemas, err = schema.LoadSchemas(s.Loader.Schemas); err != nil {
				return nil, failed(connect.CodeInternal, "the running image's schemas: %v", err)
			}
		}
		if e.changed, err = config.ChangedKinds(e.old, config.Redact(next), schemas); err != nil {
			return nil, failed(connect.CodeInternal, "compare the config with the one before the delivery that failed: %v", err)
		}
		return e, nil
	}
	if schemas != nil {
		if e.changed, err = config.ChangedKinds(old, next, schemas); err != nil {
			return nil, failed(connect.CodeInternal, "compare the config with the stored one: %v", err)
		}
	}
	before, err := s.factsFor(ctx, next, old, secret)
	if err != nil {
		return nil, err
	}
	e.before = before
	// Every fact the rules read loads now, before anything changes. One that cannot be loaded keeps
	// its error, which makes the rules that read it due.
	if err := newBaseline(old, e.factNames(), before).write(s.Paths.EffectsBaseline); err != nil {
		return nil, failed(connect.CodeInternal, "record the config and facts the consumers' rules compare with: %v", err)
	}
	e.recorded = true
	return e, nil
}

// factNames are the facts the rules read, sorted.
func (e *effects) factNames() []string {
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
func (e *effects) oldConfig() map[string]any {
	if e.oldHashed {
		return rules.HashedConfigOf(e.old)
	}
	return rules.ConfigOf(e.old)
}

// due evaluates the rules once the delivery is applied, loading the facts as they are now, and
// returns the units to reload and those to restart, each sorted. A rule that cannot be evaluated
// counts as true, and goes to the journal and to warnings, which say why without the values it
// read.
func (s *Server) due(ctx context.Context, e *effects, secret string) (reload, restart, warnings []string, err error) {
	after, err := s.factsFor(ctx, e.next, nil, secret)
	if err != nil {
		return nil, nil, nil, err
	}
	// Command facts read the old config as the rules do.
	after.Input = map[string]any{"config": rules.ConfigOf(e.next), "oldConfig": e.oldConfig()}
	due, errs := rules.Due(e.rules, rules.EffectInput{
		Documents: e.next, Old: e.old, OldHashed: e.oldHashed, ChangedKinds: e.changed, Node: e.node, Facts: after, OldFacts: e.before,
	})
	for _, err := range errs {
		w := fmt.Sprintf("the rule of %v; taking its effect all the same", err)
		log.Print(w)
		warnings = append(warnings, w)
	}
	for _, r := range due {
		if r.Action == rules.Reload {
			reload = append(reload, r.Unit)
		} else {
			restart = append(restart, r.Unit)
		}
	}
	return reload, restart, warnings, nil
}

// consume carries out what the consumers' rules ask of a delivery once it is applied. With stored
// set the delivery wrote the running slot, whose documents it writes for the consumers first; so
// does a delivery that compares with a baseline, since the one that failed part way may have
// written the slot and not its documents, which the consumers would restart on otherwise. It
// returns the restarted units and the warnings of rules that could not be evaluated. Once every
// effect is taken it removes the baseline; until then a retry compares with it and takes them
// again.
func (s *Server) consume(ctx context.Context, e *effects, stored bool, secret string) ([]string, []string, error) {
	reload, restart, warnings, err := s.due(ctx, e, secret)
	if err != nil {
		return nil, nil, err
	}
	if stored || e.oldHashed {
		if err := s.Loader.LoadConfig(); err != nil {
			// What chalkd's loops read, the slot, the cluster file and the share, is in place.
			s.actOnSelf(slices.Contains(reload, selfUnit), slices.Contains(restart, selfUnit))
			return nil, nil, failed(connect.CodeInternal, "write the config for its consumers: %v%s", err, byHand(reload, restart))
		}
	}
	restarted, err := s.applyEffects(ctx, e.consumers, reload, restart)
	if err != nil {
		return nil, nil, err
	}
	s.dropBaseline()
	return restarted, warnings, nil
}

// refused removes the baseline of a delivery refused before it changed anything, which leaves the
// next one nothing to take again. A baseline an earlier delivery that failed part way left stays.
func (s *Server) refused(e *effects) {
	if e.recorded {
		s.dropBaseline()
	}
}

// failedPartWay acts on chalkd's own rules for a delivery that failed once it changed the node
// and before its slot was written: the files its loops read besides the slot, the cluster file
// and the share, may be written by then, and the rules compare them as they are. The baseline
// stays, so a retry takes every effect of the delivery.
func (s *Server) failedPartWay(ctx context.Context, e *effects, secret string) {
	reload, restart, _, err := s.due(ctx, e, secret)
	if err != nil {
		log.Printf("the rules of chalkd's loops: %v", err)
		return
	}
	s.actOnSelf(slices.Contains(reload, selfUnit), slices.Contains(restart, selfUnit))
}

// applyEffects reloads the units of reload that run, then restarts those of restart, and returns
// the restarted units; chalkd itself acts on its own rules last. A unit it restarts is not
// reloaded: the restart reads all a reload would.
// The restarts take one systemctl call each way: try-restart for a unit that runs, or ran and
// stays active, so a unit that never started, or was stopped, stays so; restart for a unit that
// starts too; and for a unit that failed, such as one that found no document of its kind at boot,
// reset-failed and restart, since the new config may be what it lacked and try-restart leaves a
// failed unit alone.
func (s *Server) applyEffects(ctx context.Context, consumers map[string]nodeconfig.Consumer, reload, restart []string) ([]string, error) {
	isSelf := func(unit string) bool { return unit == selfUnit }
	reloadSelf, restartSelf := slices.ContainsFunc(reload, isSelf), slices.ContainsFunc(restart, isSelf)
	reload = slices.DeleteFunc(slices.Clone(reload), isSelf)
	restart = slices.DeleteFunc(slices.Clone(restart), isSelf)
	// Once the units it restarts read the new config, and whether or not they could: the
	// cluster and the share the loops read are in place.
	defer s.actOnSelf(reloadSelf, restartSelf)
	states := map[string]string{}
	for _, unit := range append(slices.Clone(reload), restart...) {
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
	var reloadByHand, restartByHand []string
	for _, unit := range reload {
		// A unit that does not run reads its config when it starts.
		if slices.Contains(restart, unit) || states[unit] == "inactive" || states[unit] == "failed" {
			continue
		}
		argv := strings.Fields(consumers[unit].Reload.Command)
		if _, err := s.Run.Run(ctx, argv[0], argv[1:]...); err != nil {
			reloadByHand = append(reloadByHand, unit)
			errs = append(errs, fmt.Errorf("reload %s: %w", unit, err))
		}
	}
	var failedUnits, start, try []string
	for _, unit := range restart {
		switch {
		case states[unit] == "failed":
			failedUnits = append(failedUnits, unit)
		case consumers[unit].Restart.Start:
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
			restartByHand = append(restartByHand, units...)
			errs = append(errs, fmt.Errorf("%s %s: %w", verb, strings.Join(units, " "), err))
			return
		}
		restarted = append(restarted, units...)
	}
	if len(failedUnits) > 0 {
		// The failures that put them in their state count against their start limit, which would
		// refuse the restart.
		if _, err := s.Run.Run(ctx, "systemctl", append([]string{"reset-failed"}, failedUnits...)...); err != nil {
			restartByHand = append(restartByHand, failedUnits...)
			errs = append(errs, fmt.Errorf("reset-failed %s: %w", strings.Join(failedUnits, " "), err))
			failedUnits = nil
		}
	}
	start = append(failedUnits, start...)
	slices.Sort(start)
	run("restart", start)
	run("try-restart", try)
	if len(errs) > 0 {
		slices.Sort(restartByHand)
		hand := byHand(reloadByHand, restartByHand)
		// The Kubernetes units start only once their preparation succeeds, whose reason tells
		// more than systemctl's error; another unit's failure has its own.
		if slices.ContainsFunc(restartByHand, isKubernetesUnit) && s.Kubernetes != nil {
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
	return unit == "chalkos-kubernetes" || unit == "kubelet"
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

// byHand tells the operator which units still need what the delivery does to them.
func byHand(reload, restart []string) string {
	var todo []string
	if len(reload) > 0 {
		todo = append(todo, "reload "+strings.Join(reload, " "))
	}
	if len(restart) > 0 {
		todo = append(todo, "restart "+strings.Join(restart, " "))
	}
	if len(todo) == 0 {
		return ""
	}
	return "; the delivery is applied, so " + strings.Join(todo, " and ") + " by hand once that is fixed, or deliver it again"
}
