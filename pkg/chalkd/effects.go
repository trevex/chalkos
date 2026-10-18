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
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// selfUnit is the consumer that is chalkd itself, which acts on its rules in-process: a reload
// starts the control plane's loop again, which reads the share and applies the control plane's
// objects, and a restart every Kubernetes loop, which read the cluster when they start.
const selfUnit = "chalkd"

// effects are the restart and reload rules of the running image's consumers, and what a delivery
// compares them with: the kinds it changes and the facts as they were before it changed anything.
type effects struct {
	consumers map[string]nodeconfig.Consumer
	rules     []*rules.Effect
	next, old []config.Document
	changed   []string
	before    *facts.Loader
}

// effectsOf compiles the rules of the running image's consumers for a delivery that makes next,
// checked against schemas, the config of the running slot in place of old, and loads every fact
// they read: the caller calls it before the delivery changes anything. A delivery without config
// keeps old, which changes no kind. The image's build compiled the rules already, so one that
// does not compile is the node's own failure.
func (s *Server) effectsOf(ctx context.Context, next, old []config.Document, schemas *schema.Schemas, secret string) (*effects, error) {
	consumers, err := nodeconfig.ReadConsumers(s.Loader.Consumers)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	e := &effects{consumers: consumers, next: next, old: old}
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
	if schemas != nil {
		if e.changed, err = config.ChangedKinds(old, next, schemas); err != nil {
			return nil, failed(connect.CodeInternal, "compare the config with the stored one: %v", err)
		}
	}
	if e.before, err = s.factsFor(ctx, next, old, secret); err != nil {
		return nil, err
	}
	// A fact that cannot be loaded keeps its error, which makes the rules that read it due.
	for _, name := range e.factNames() {
		_, _ = e.before.Fact(name)
	}
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

// due evaluates the rules once the delivery is applied, with after the facts as they are now, and
// returns the units to reload and those to restart, each sorted. A rule that cannot be evaluated
// counts as true, and goes to the journal.
func (e *effects) due(after rules.Facts) (reload, restart []string) {
	due, errs := rules.Due(e.rules, rules.EffectInput{
		Documents: e.next, Old: e.old, ChangedKinds: e.changed, Facts: after, OldFacts: e.before,
	})
	for _, err := range errs {
		log.Printf("the rule of %v; taking its effect all the same", err)
	}
	for _, r := range due {
		if r.Action == rules.Reload {
			reload = append(reload, r.Unit)
		} else {
			restart = append(restart, r.Unit)
		}
	}
	return reload, restart
}

// consume carries out what the consumers' rules ask of a delivery once it is applied. With stored
// set the delivery wrote the running slot, whose documents it writes for the consumers first. It
// returns the restarted units.
func (s *Server) consume(ctx context.Context, e *effects, stored bool, secret string) ([]string, error) {
	after, err := s.factsFor(ctx, e.next, e.old, secret)
	if err != nil {
		return nil, err
	}
	reload, restart := e.due(after)
	if stored {
		// From here on the slot holds the new set, so a retry compares it with itself and
		// restarts nothing. A failure therefore names the units that still need what the
		// delivery does to them, rather than leaving a node whose units read the old config
		// without saying so.
		if err := s.Loader.LoadConfig(); err != nil {
			return nil, failed(connect.CodeInternal, "write the config for its consumers: %v%s", err, byHand(reload, restart))
		}
	}
	return s.applyEffects(ctx, e.consumers, reload, restart)
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
		// more than systemctl's error.
		if len(restartByHand) > 0 && s.Kubernetes != nil {
			if problem, err := preparation(s.Kubernetes.Paths); err == nil && problem != "" && problem != preparing {
				return nil, failed(connect.CodeFailedPrecondition, "%v; the node runs no kubelet: %s%s", errors.Join(errs...), problem, hand)
			}
		}
		return nil, failed(connect.CodeInternal, "%v%s", errors.Join(errs...), hand)
	}
	slices.Sort(restarted)
	return restarted, nil
}

// actOnSelf reloads or restarts chalkd's Kubernetes loops, on a node with Kubernetes; a restart
// starts the control plane's loop again too.
func (s *Server) actOnSelf(reload, restart bool) {
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
	return "; the delivery is applied, so " + strings.Join(todo, " and ") + " by hand once that is fixed"
}
