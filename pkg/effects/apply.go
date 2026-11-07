package effects

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// Actions are what the due effects ask for together:
//   - Reload and Restart: the units to reload and to restart, each sorted;
//   - Start: the units of Restart to start too;
//   - Run: the commands to run, in the order of their effects' names;
//   - Chalkd: the layer actions to run, sorted;
//   - Hints: the hint of each unit in the restart of any effect of the image, due or not.
type Actions struct {
	Reload, Restart []string
	Start           map[string]bool
	Run             []string
	Chalkd          []string
	Hints           map[string]Hint
}

// ActionsOf is what the effects due, of the image's defs, ask for together.
func ActionsOf(defs map[string]Definition, due []string) Actions {
	a := Actions{Start: map[string]bool{}, Hints: map[string]Hint{}}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		if d := defs[name]; d.Hint != nil {
			for _, unit := range d.Restart {
				a.Hints[unit] = *d.Hint
			}
		}
	}
	for _, name := range due {
		d := defs[name]
		a.Reload = append(a.Reload, d.Reload...)
		a.Restart = append(a.Restart, d.Restart...)
		for _, unit := range d.Restart {
			a.Start[unit] = a.Start[unit] || d.Start
		}
		if d.Run != "" && !slices.Contains(a.Run, d.Run) {
			a.Run = append(a.Run, d.Run)
		}
		if d.Chalkd != "" {
			a.Chalkd = append(a.Chalkd, d.Chalkd)
		}
	}
	slices.Sort(a.Reload)
	slices.Sort(a.Restart)
	slices.Sort(a.Chalkd)
	a.Reload, a.Restart, a.Chalkd = slices.Compact(a.Reload), slices.Compact(a.Restart), slices.Compact(a.Chalkd)
	return a
}

// ByHand tells the operator what the delivery still has to do to the units.
func (a Actions) ByHand() string {
	var todo []string
	if len(a.Reload) > 0 {
		todo = append(todo, "reload "+strings.Join(a.Reload, " "))
	}
	for _, command := range a.Run {
		todo = append(todo, "run "+command)
	}
	if len(a.Restart) > 0 {
		todo = append(todo, "restart "+strings.Join(a.Restart, " "))
	}
	if len(todo) == 0 {
		return ""
	}
	return "; the delivery is applied, so " + strings.Join(todo, " and ") + " by hand once that is fixed, or deliver it again"
}

// ApplyError is what Apply could not do: Err, the failures joined, and ByHand, what the delivery
// still has to do to the units. Why says why a unit that did not restart may fail to start, as its
// effect's hint tells, which tells more than systemctl's error; "" when no hint does.
type ApplyError struct {
	Err    error
	Why    string
	ByHand Actions
}

func (e *ApplyError) Error() string {
	if e.Why != "" {
		return fmt.Sprintf("%v; %s%s", e.Err, e.Why, e.ByHand.ByHand())
	}
	return fmt.Sprintf("%v%s", e.Err, e.ByHand.ByHand())
}

func (e *ApplyError) Unwrap() error { return e.Err }

// Apply reloads the units of a.Reload that run, runs a.Run, then restarts the units of a.Restart,
// and returns the restarted units; the caller runs the layer actions of a.Chalkd. A unit it
// restarts is not reloaded, because the restart reads all a reload would. A unit of a.Reload that
// failed is restarted as a failed unit of a.Restart is: systemctl reloads no failed unit, and its
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
//
// What fails is an *ApplyError; the rest is done regardless.
func Apply(ctx context.Context, r tool.Runner, a Actions) ([]string, error) {
	states := map[string]string{}
	for _, unit := range append(slices.Clone(a.Reload), a.Restart...) {
		if _, ok := states[unit]; ok {
			continue
		}
		state, err := systemctl.ActiveState(ctx, r, unit)
		if err != nil {
			log.Printf("the state of %s: %v; acting on it as if it ran", unit, err)
		}
		states[unit] = state
	}
	var errs []error
	var reload, failedUnits []string
	for _, unit := range a.Reload {
		if slices.Contains(a.Restart, unit) {
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
	for _, unit := range a.Restart {
		switch {
		case states[unit] == "failed":
			failedUnits = append(failedUnits, unit)
		case a.Start[unit]:
			start = append(start, unit)
		default:
			try = append(try, unit)
		}
	}
	slices.Sort(failedUnits)
	slices.Sort(start)
	inOrder := unitOrder(ctx, r, reload, append(slices.Clone(failedUnits), start...), try)
	byHand := Actions{}
	reload = inOrder(reload)
	if len(reload) > 0 {
		if _, err := tool.Run(ctx, r, "systemctl", append([]string{"reload"}, reload...)...); err != nil {
			byHand.Reload = reload
			errs = append(errs, fmt.Errorf("reload %s: %w", strings.Join(reload, " "), err))
		}
	}
	for _, command := range a.Run {
		argv := strings.Fields(command)
		if _, err := tool.Run(ctx, r, argv[0], argv[1:]...); err != nil {
			byHand.Run = append(byHand.Run, command)
			errs = append(errs, fmt.Errorf("run %s: %w", command, err))
		}
	}
	var restarted []string
	run := func(verb string, units []string) {
		if len(units) == 0 {
			return
		}
		if _, err := tool.Run(ctx, r, "systemctl", append([]string{verb}, units...)...); err != nil {
			byHand.Restart = append(byHand.Restart, units...)
			errs = append(errs, fmt.Errorf("%s %s: %w", verb, strings.Join(units, " "), err))
			return
		}
		restarted = append(restarted, units...)
	}
	if len(failedUnits) > 0 {
		// The failures that put them in their state count against their start limit, which would
		// refuse the restart.
		if _, err := tool.Run(ctx, r, "systemctl", append([]string{"reset-failed"}, failedUnits...)...); err != nil {
			byHand.Restart = append(byHand.Restart, failedUnits...)
			errs = append(errs, fmt.Errorf("reset-failed %s: %w", strings.Join(failedUnits, " "), err))
			failedUnits = nil
		}
	}
	start = append(failedUnits, start...)
	slices.Sort(start)
	run("restart", inOrder(start))
	run("try-restart", inOrder(try))
	if len(errs) > 0 {
		slices.Sort(byHand.Restart)
		e := &ApplyError{Err: errors.Join(errs...), ByHand: byHand}
		for _, unit := range byHand.Restart {
			if h, ok := a.Hints[unit]; ok {
				if why := hintText(h); why != "" {
					e.Why = why
					break
				}
			}
		}
		return nil, e
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
// left, so units ordered against none of the others keep their place. Once only units that follow
// one another in a cycle, or follow such units, are left, they all keep their order, after the
// others. Every list keeps the order given when systemctl does not answer, or answers for other
// units than those asked for, as for a glob.
func unitOrder(ctx context.Context, r tool.Runner, lists ...[]string) func([]string) []string {
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
	shown, err := systemctl.Show(ctx, r, units, "Names", "After")
	if err == nil && len(shown) != len(units) {
		err = fmt.Errorf("systemctl showed %d units", len(shown))
	}
	names := map[string][]string{}
	after := map[string][]string{}
	if err == nil {
		for i, props := range shown {
			names[units[i]] = strings.Fields(props["Names"])
			after[units[i]] = strings.Fields(props["After"])
			// A glob, which an effect may name, shows the units it matches instead.
			if !slices.Contains(names[units[i]], units[i]) {
				err = fmt.Errorf("systemctl showed %s where %s was asked for", cmp.Or(strings.Join(names[units[i]], " "), "no unit"), units[i])
				break
			}
		}
	}
	if err != nil {
		log.Printf("the order of %s: %v; acting on them in the order given", strings.Join(units, " "), err)
		return keep
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
func hintText(h Hint) string {
	if h.File == "" {
		return h.Text
	}
	data, err := os.ReadFile(h.File)
	if why := strings.TrimSpace(string(data)); err == nil && why != "" {
		return h.Text + ": " + why
	}
	return ""
}
