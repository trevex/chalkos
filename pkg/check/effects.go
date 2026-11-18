package check

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// Effects are an image's effects, by name, and the rules of those that have one, compiled.
type Effects struct {
	Defs  map[string]effects.Definition
	Rules []*rules.EffectRule
}

// Delivery is what a delivery gives the node, as the effects compare it: the config it runs once
// the delivery is applied, with its defaults, the schemas that tell its secretRef fields, and the
// states of the outputs rendered from it, by name.
type Delivery struct {
	Node      string
	Documents config.Redacted
	Schemas   *schema.Schemas
	Outputs   map[string]outputs.State
}

// Comparison is a state the effects compare a delivery with: the documents the node ran, the facts
// as they are once the delivery is applied and as they were, the states of the outputs and the
// hashes of the kinds.
type Comparison struct {
	Documents       config.Redacted
	Facts, OldFacts rules.Facts
	Outputs         map[string]outputs.State
	Kinds           map[string]string
}

// Outcome is what a delivery changes, as the effects compare it: the outputs whose state differs
// from that of a comparison, sorted, and the effects it takes, sorted by name.
type Outcome struct {
	Outputs []string
	Effects []Fired
}

// Fired is an effect a delivery takes, and what fired it, each as "output <name>", "kind <name>"
// or "rule": its outputs in the order the effect names them, then its kinds, then its rule.
type Fired struct {
	Name string
	By   []string
}

// Due compares d with each of olds and returns the outputs and effects it changes. An effect is
// due when, compared with one of olds, one of its outputs' states differs, the documents of one of
// its kinds or a Secret they reference differ, or its rule holds. A rule that cannot be evaluated
// counts as true, so its effect runs; a warning says why, without the values it read.
func Due(fx Effects, d Delivery, olds []Comparison) (o Outcome, warnings []string, err error) {
	kinds, err := config.KindHashes(d.Documents, d.Schemas)
	if err != nil {
		return Outcome{}, nil, fmt.Errorf("the kinds of the delivery: %w", err)
	}
	changed := func(name string) bool {
		st, ok := d.Outputs[name]
		return ok && slices.ContainsFunc(olds, func(old Comparison) bool { return st != old.Outputs[name] })
	}
	for _, name := range slices.Sorted(maps.Keys(d.Outputs)) {
		if changed(name) {
			o.Outputs = append(o.Outputs, name)
		}
	}
	ruled := map[string]bool{}
	for _, old := range olds {
		due, errs := rules.Due(fx.Rules, rules.EffectInput{
			Documents: d.Documents, Old: old.Documents, Node: d.Node, Facts: old.Facts, OldFacts: old.OldFacts,
		})
		for _, err := range errs {
			if w := fmt.Sprintf("the rule of %v; taking the effect all the same", err); !slices.Contains(warnings, w) {
				warnings = append(warnings, w)
			}
		}
		for _, r := range due {
			ruled[r.Name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fx.Defs)) {
		def := fx.Defs[name]
		var by []string
		for _, out := range def.Outputs {
			if changed(out) {
				by = append(by, "output "+out)
			}
		}
		for _, k := range def.Kinds {
			if slices.ContainsFunc(olds, func(old Comparison) bool { return kinds[k] != old.Kinds[k] }) {
				by = append(by, "kind "+k)
			}
		}
		if ruled[name] {
			by = append(by, "rule")
		}
		if len(by) > 0 {
			o.Effects = append(o.Effects, Fired{Name: name, By: by})
		}
	}
	return o, warnings, nil
}

// Plan is what a delivery does once it is applied: the outputs it changes and the effects it
// takes.
type Plan struct {
	Outputs []string
	Effects []PlannedEffect
}

// PlannedEffect is an effect a delivery takes: its name, what fired it and what it does, as
// "reload <units>", "run <command>", "restart <units>" and "chalkd <action>", in the order chalkd
// does them.
type PlannedEffect struct {
	Name    string
	FiredBy []string
	Actions []string
}

// PlanOf is the plan of a delivery that changes o, with the effects of fx.
func PlanOf(fx Effects, o Outcome) (Plan, error) {
	p := Plan{Outputs: o.Outputs}
	var names []string
	var order []effects.ScheduledEffect
	fired := map[string][]string{}
	for _, f := range o.Effects {
		names = append(names, f.Name)
		order = append(order, effects.ScheduledEffect{Name: f.Name})
		fired[f.Name] = f.By
	}
	schedule, err := effects.PlanSchedule(fx.Defs, names)
	if err != nil {
		return Plan{}, err
	}
	if schedule.Ordered() {
		order = schedule.Order()
	}
	for _, effect := range order {
		d := fx.Defs[effect.Name]
		var steps []string
		if len(effect.After) != 0 {
			steps = append(steps, "after "+strings.Join(effect.After, ", "))
		}
		if len(effect.SharedWith) != 0 {
			steps = append(steps, "shares actions with "+strings.Join(effect.SharedWith, ", "))
		}
		if d.WaitFor != nil {
			steps = append(steps, fmt.Sprintf("wait for %s (up to %ds)", d.WaitFor.Description, d.WaitFor.TimeoutSeconds))
		}
		p.Effects = append(p.Effects, PlannedEffect{Name: effect.Name, FiredBy: fired[effect.Name], Actions: append(steps, actions(d)...)})
	}
	return p, nil
}

// actions are what an effect does, in the order chalkd does it for every effect together:
// reloads, commands, restarts, then its layer action.
func actions(d effects.Definition) []string {
	var out []string
	if len(d.Reload) > 0 {
		out = append(out, "reload "+strings.Join(d.Reload, " "))
	}
	if d.Run != "" {
		out = append(out, "run "+d.Run)
	}
	if len(d.Restart) > 0 {
		out = append(out, "restart "+strings.Join(d.Restart, " "))
	}
	if d.Chalkd != "" {
		out = append(out, "chalkd "+d.Chalkd)
	}
	return out
}
