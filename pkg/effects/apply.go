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

// Actions are what the due effects ask for together. Run keeps the order of the effects' names;
// Hints holds the hint of each unit any effect of the image restarts, due or not.
type Actions struct {
	Reload, Restart []string
	Start           map[string]bool
	Run             [][]string
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
		if len(d.Run) > 0 && !slices.ContainsFunc(a.Run, func(run []string) bool { return slices.Equal(run, d.Run) }) {
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
	for _, argv := range a.Run {
		quoted := make([]string, len(argv))
		for i, arg := range argv {
			quoted[i] = shellQuote(arg)
		}
		todo = append(todo, "run "+strings.Join(quoted, " "))
	}
	if len(a.Restart) > 0 {
		todo = append(todo, "restart "+strings.Join(a.Restart, " "))
	}
	if len(todo) == 0 {
		return ""
	}
	return "; the delivery is applied, so " + strings.Join(todo, " and ") + " by hand once that is fixed, or deliver it again"
}

// shellQuote puts an argument in single quotes when it is empty or a shell would split or expand it.
func shellQuote(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n\"'\\$`;&|<>()*?[]{}~#!") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
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

// Apply reloads the units of a.Reload that run, runs a.Run, then restarts the units of a.Restart
// and returns them; the caller runs a.Chalkd. A restarted unit is not reloaded too, because the
// restart reads all a reload would, and a failed unit of a.Reload is restarted, since systemctl
// reloads no failed unit. A unit restarts after the reloads, so after units ordered after it: a
// unit whose reload depends on such a unit's work asks for that work itself, such as by restarting
// the unit. The restarts take one systemctl call per mode:
//   - try-restart for a unit that runs, so a unit that never started or was stopped stays so;
//   - restart for a unit whose effect starts it too;
//   - reset-failed and restart for a failed unit, which the new config may fix and try-restart
//     leaves alone.
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
	for _, argv := range a.Run {
		if _, err := tool.Run(ctx, r, argv[0], argv[1:]...); err != nil {
			byHand.Run = append(byHand.Run, argv)
			errs = append(errs, fmt.Errorf("run %s: %w", strings.Join(argv, " "), err))
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

// unitOrder returns a sort of the units of lists by their After= ordering. systemctl queues each
// unit's job in a transaction of its own, and systemd holds a job back only for jobs already
// queued, so the order given decides; it holds within one call alone, so the reloads run before
// any restart. Before= of other units counts too, as systemd shows it as After; a unit given by an
// alias matches through its Names. Units ordered against none keep their place, and every list
// keeps its order when systemctl does not answer or answers for other units, as for a glob. Units
// in a cycle keep their order, after the others.
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
