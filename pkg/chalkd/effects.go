package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/effects"
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
	// attempts are what the deliveries that failed part way since the baseline was recorded
	// applied, which the rules, outputs and kinds compare with too.
	attempts []baselineAttempt
	// attempt is what this delivery applies, as recordAttempt added it to the baseline; nil until
	// then. withoutAttempt is the baseline as it was before, which refused restores.
	attempt        *baselineAttempt
	withoutAttempt []byte
}

// effectsOf compiles the rules of the running image's effects and loads every fact they read.
// The caller calls it before the delivery changes anything.
//   - The delivery goes to the node called name and makes next, checked against schemas, the
//     running slot's config in place of old. A delivery without config keeps old.
//   - The image's build compiled the rules already, so one that does not compile is the node's
//     own failure.
//   - While a baseline exists, as a delivery that failed part way leaves one, the rules compare
//     with it, and with what each delivery that failed since applied. Otherwise effectsOf
//     records old, the facts, the outputs' states and the kinds' hashes as the baseline.
//
// consume removes the baseline once the delivery is applied whole, and refused once it is refused
// before it changed anything. Until then a retry, or another delivery, takes the effects of what
// the failed delivery changed too, even one that delivers the baseline's config again.
func (s *Server) effectsOf(ctx context.Context, name string, next, old []config.Document, schemas *schema.Schemas, secret string) (*pendingEffects, error) {
	defs, err := effects.Read(s.Loader.Effects)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	// An action no active plugin has would fail only once the delivery changed the node.
	for _, effect := range slices.Sorted(maps.Keys(defs)) {
		if a := defs[effect].Chalkd; a != "" {
			if _, err := s.Plugins.Action(a); err != nil {
				return nil, failed(connect.CodeInternal, "%s: effect %s: %v", s.Loader.Effects, effect, err)
			}
		}
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
		e.old, e.oldHashed, e.before, e.outputs, e.kinds, e.attempts = b.documents(), true, b.Facts, b.Outputs, b.Kinds, b.Attempts
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

// due returns the sorted names of the effects due once the delivery is applied. An effect is due
// when, compared with before the delivery or with what a delivery that failed part way since
// applied:
//   - one of its outputs has a state in states, the outputs swapped in, that differs;
//   - the documents of one of its kinds, or a Secret they reference, differ;
//   - its rule holds on the facts as they are now, which it compares with the facts from before
//     the delivery or with those the failed delivery loaded.
//
// A rule that cannot be evaluated counts as true. Its error goes to the journal and to warnings,
// which say why without the values it read. The facts as they are now go to the delivery's
// attempt in the baseline, which recordAttempt added.
func (s *Server) due(ctx context.Context, e *pendingEffects, states map[string]outputs.State, secret string) (due, warnings []string, err error) {
	after, err := s.factsFor(ctx, e.next, nil, secret)
	if err != nil {
		return nil, nil, err
	}
	// Command facts read the old config as the rules do.
	after.Input = map[string]any{"config": rules.ConfigOf(e.next), "oldConfig": e.oldConfig()}
	if e.attempt != nil {
		// The delivery changed the node by now, so the facts are those its units read.
		if err := setAttemptFacts(s.Paths.EffectsBaseline, *e.attempt, recordFacts(e.factNames(), after)); err != nil {
			// Without them every rule that reads a fact compares as due with this attempt.
			log.Printf("record the facts of the delivery in the baseline of the effects: %v", err)
		}
	}
	kinds, err := config.KindHashes(e.next, e.schemas)
	if err != nil {
		return nil, nil, failed(connect.CodeInternal, "the kinds of the delivery: %v", err)
	}
	olds := []comparison{{docs: e.old, hashed: e.oldHashed, facts: after, oldFacts: e.before, outputs: e.outputs, kinds: e.kinds}}
	for _, a := range e.attempts {
		docs := a.documents()
		// Command facts read the attempt's config as the old one.
		facts := after.For(map[string]any{"config": rules.ConfigOf(e.next), "oldConfig": rules.HashedConfigOf(docs)})
		olds = append(olds, comparison{docs: docs, hashed: true, facts: facts, oldFacts: a.Facts, outputs: a.Outputs, kinds: a.Kinds})
	}
	for _, old := range olds {
		ruled, errs := rules.Due(e.rules, rules.EffectInput{
			Documents: e.next, Old: old.docs, OldHashed: old.hashed, Node: e.node, Facts: old.facts, OldFacts: old.oldFacts,
		})
		for _, err := range errs {
			w := fmt.Sprintf("the rule of %v; taking the effect all the same", err)
			if !slices.Contains(warnings, w) {
				log.Print(w)
				warnings = append(warnings, w)
			}
		}
		for _, r := range ruled {
			due = append(due, r.Name)
		}
		for name, d := range e.defs {
			changedOutput := slices.ContainsFunc(d.Outputs, func(o string) bool { st, ok := states[o]; return ok && st != old.outputs[o] })
			changedKind := slices.ContainsFunc(d.Kinds, func(k string) bool { return kinds[k] != old.kinds[k] })
			if changedOutput || changedKind {
				due = append(due, name)
			}
		}
	}
	slices.Sort(due)
	return slices.Compact(due), warnings, nil
}

// comparison is a state due compares a delivery with: the old documents, hashed already or not,
// the facts as they are now and as they were, the states of the outputs and the hashes of the
// kinds.
type comparison struct {
	docs            []config.Document
	hashed          bool
	facts, oldFacts rules.Facts
	outputs         map[string]outputs.State
	kinds           map[string]string
}

// recordAttempt adds what the delivery of e applies to the baseline before the delivery changes
// anything: its documents, the states of the outputs it rendered, states, and the hashes of its
// kinds; due adds the facts once the delivery loaded them. Should it fail or stop part way, as a
// crash stops it, a unit it restarted runs its config, so the next delivery takes the effects
// that compare with it too, even one of the config from before it. The attempts go with the
// baseline once a delivery is applied whole.
func (s *Server) recordAttempt(e *pendingEffects, states map[string]outputs.State) error {
	kinds, err := config.KindHashes(e.next, e.schemas)
	if err != nil {
		// Without them every effect on a kind the delivery holds is due.
		log.Printf("the kinds of the delivery: %v", err)
	}
	a := baselineAttempt{Documents: redacted(e.next), Outputs: states, Kinds: kinds}
	previous, err := addAttempt(s.Paths.EffectsBaseline, a)
	if err != nil {
		return failed(connect.CodeInternal, "record the delivery in the baseline of the effects: %v", err)
	}
	e.attempt, e.withoutAttempt = &a, previous
	return nil
}

// consume takes the effects a delivery asks for once it is applied, in this order:
//  1. the running slot's documents for the granted units, when stored says the delivery wrote the
//     slot, or when it compares with a baseline: the delivery that failed part way may have
//     written the slot and not its documents, which the units would otherwise restart on;
//  2. the staged outputs;
//  3. the effects.
//
// It returns the restarted units and the warnings of rules that could not be evaluated. Once every
// effect is taken it removes the baseline; until then a retry compares with it and takes them
// again.
func (s *Server) consume(ctx context.Context, e *pendingEffects, staged *outputs.Staged, stored bool, secret string) ([]string, []string, error) {
	states := staged.States()
	due, warnings, err := s.due(ctx, e, states, secret)
	if err != nil {
		return nil, nil, err
	}
	if stored || e.oldHashed {
		if err := s.Loader.LoadConfig(); err != nil {
			// What the layer actions read, the slot and the layers' files, is in place.
			a := actionsOf(e.defs, due)
			s.actOnSelf(a.chalkd)
			return nil, nil, failed(connect.CodeInternal, "write the config for the granted units: %v%s", err, a.byHand())
		}
	}
	// The outputs, checked before the delivery changed anything, follow the documents.
	if err := staged.Swap(); err != nil {
		a := actionsOf(e.defs, due)
		s.actOnSelf(a.chalkd)
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
	a := actionsOf(e.defs, due)
	s.actOnSelf(a.chalkd)
}

// actions are what the due effects ask for together:
//   - reload and restart: the units to reload and to restart, each sorted;
//   - start: the units of restart to start too;
//   - run: the commands to run, in the order of their effects' names;
//   - chalkd: the layer actions to run, sorted;
//   - hints: the hint of each unit in the restart of any effect of the image, due or not.
type actions struct {
	reload, restart []string
	start           map[string]bool
	run             []string
	chalkd          []string
	hints           map[string]effects.Hint
}

func actionsOf(defs map[string]effects.Definition, due []string) actions {
	a := actions{start: map[string]bool{}, hints: map[string]effects.Hint{}}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		if d := defs[name]; d.Hint != nil {
			for _, unit := range d.Restart {
				a.hints[unit] = *d.Hint
			}
		}
	}
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
		if d.Chalkd != "" {
			a.chalkd = append(a.chalkd, d.Chalkd)
		}
	}
	slices.Sort(a.reload)
	slices.Sort(a.restart)
	slices.Sort(a.chalkd)
	a.reload, a.restart, a.chalkd = slices.Compact(a.reload), slices.Compact(a.restart), slices.Compact(a.chalkd)
	return a
}

// applyEffects reloads the units of a.reload that run, runs a.run, then restarts the units of
// a.restart, and returns the restarted units. chalkd runs the layer actions last. A unit it
// restarts is not reloaded, because the restart reads all a reload would. A unit of a.reload that
// failed is restarted as a failed unit of a.restart is: systemctl reloads no failed unit, and its
// start, such as a firewall's that loads the new ruleset, is what the new config needs. It
// restarts after the reloads, so after units that are ordered after it: a unit whose reload
// depends on such a unit's work has that unit ask for it, as chalkos-vrf asks chalkos-resolvers.
//
// The restarts take one systemctl call for each way of restarting, each in the units' order:
//   - try-restart for a unit that runs, or ran and stays active, so a unit that never started,
//     or was stopped, stays so;
//   - restart for a unit whose effect starts it too;
//   - reset-failed and restart for a unit that failed, such as one that found no document of its
//     kind at boot. The new config may be what it lacked, and try-restart leaves a failed unit
//     alone.
func (s *Server) applyEffects(ctx context.Context, a actions) ([]string, error) {
	// Once the units it restarts read the new config, and whether or not they could: the files
	// the layer actions read are in place.
	defer s.actOnSelf(a.chalkd)
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
	var reload, failedUnits []string
	for _, unit := range a.reload {
		if slices.Contains(a.restart, unit) {
			continue
		}
		// A unit that does not run reads its config when it starts; a failed one is restarted.
		if states[unit] == "failed" {
			failedUnits = append(failedUnits, unit)
			continue
		}
		if states[unit] == "inactive" {
			continue
		}
		reload = append(reload, unit)
	}
	var start, try []string
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
	slices.Sort(failedUnits)
	slices.Sort(start)
	inOrder := s.unitOrder(ctx, reload, append(slices.Clone(failedUnits), start...), try)
	byHand := actions{}
	reload = inOrder(reload)
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
	run("restart", inOrder(start))
	run("try-restart", inOrder(try))
	if len(errs) > 0 {
		slices.Sort(byHand.restart)
		hand := byHand.byHand()
		// A unit whose effect says why it may fail to start, which tells more than systemctl's
		// error, says so.
		for _, unit := range byHand.restart {
			if h, ok := a.hints[unit]; ok {
				if why := hintText(h); why != "" {
					return nil, failed(connect.CodeFailedPrecondition, "%v; %s%s", errors.Join(errs...), why, hand)
				}
			}
		}
		return nil, failed(connect.CodeInternal, "%v%s", errors.Join(errs...), hand)
	}
	slices.Sort(restarted)
	return restarted, nil
}

// unitOrder returns what sorts a list of the units of lists so that each unit follows the units
// of the list it is ordered after, by its After= or their Before=, which systemd shows as its
// After. systemctl queues the job of each unit it is given in a transaction of its own, one after
// the other, and systemd holds a job back only for the jobs already queued: a job queued first
// runs at once, whatever its unit's order. The order holds within one systemctl call alone, so
// the reloads run before any restart, whatever the units' order.
//
// systemctl names the units of After by their main names, which a unit given by an alias matches
// through its Names. Each step of the sort takes the first unit that follows none of the units
// left, so units ordered against none of the others keep their place. The units of a cycle keep
// theirs after the others, and the units of every list keep the order given when systemctl does
// not answer.
func (s *Server) unitOrder(ctx context.Context, lists ...[]string) func([]string) []string {
	keep := func(units []string) []string { return units }
	var units []string
	ask := false
	for _, l := range lists {
		ask = ask || len(l) > 1
		for _, u := range l {
			if !slices.Contains(units, u) {
				units = append(units, u)
			}
		}
	}
	if !ask {
		return keep
	}
	out, err := s.Run.Run(ctx, "systemctl", append([]string{"show", "--property=Names,After"}, units...)...)
	// systemctl shows each unit's properties as lines of name=value, the units in the order
	// given, a blank line between them.
	blocks := strings.Split(strings.TrimSpace(string(out)), "\n\n")
	if err == nil && len(blocks) != len(units) {
		err = fmt.Errorf("systemctl showed %d units", len(blocks))
	}
	if err != nil {
		log.Printf("the order of %s: %v; acting on them in the order given", strings.Join(units, " "), err)
		return keep
	}
	names := map[string][]string{}
	after := map[string][]string{}
	for i, block := range blocks {
		for line := range strings.SplitSeq(block, "\n") {
			if v, ok := strings.CutPrefix(line, "Names="); ok {
				names[units[i]] = strings.Fields(v)
			} else if v, ok := strings.CutPrefix(line, "After="); ok {
				after[units[i]] = strings.Fields(v)
			}
		}
	}
	// follows tells whether the unit u is ordered after the unit v.
	follows := func(u, v string) bool {
		return u != v && slices.ContainsFunc(after[u], func(d string) bool { return d == v || slices.Contains(names[v], d) })
	}
	return func(list []string) []string {
		rest := slices.Clone(list)
		sorted := make([]string, 0, len(list))
		for len(rest) > 0 {
			i := slices.IndexFunc(rest, func(u string) bool {
				return !slices.ContainsFunc(rest, func(v string) bool { return follows(u, v) })
			})
			if i < 0 {
				return append(sorted, rest...)
			}
			sorted = append(sorted, rest[i])
			rest = slices.Delete(rest, i, i+1)
		}
		return sorted
	}
}

// hintText is a hint's text, followed by its file's while the file holds text; "" when its file
// holds none.
func hintText(h effects.Hint) string {
	if h.File == "" {
		return h.Text
	}
	data, err := os.ReadFile(h.File)
	if why := strings.TrimSpace(string(data)); err == nil && why != "" {
		return h.Text + ": " + why
	}
	return ""
}

// actOnSelf runs the layer actions the delivery's effects name, on what only the running chalkd
// holds.
func (s *Server) actOnSelf(names []string) {
	if s.actedOnSelf != nil {
		s.actedOnSelf(names)
	}
	for _, name := range names {
		run, err := s.Plugins.Action(name)
		if err == nil {
			err = run(context.Background())
		}
		if err != nil {
			log.Printf("%s: %v", name, err)
		}
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
