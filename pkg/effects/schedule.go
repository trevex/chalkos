package effects

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/command"
	"github.com/trevex/chalkos/pkg/tool"
)

// Readiness is a read-only, image-owned probe run before an effect. Exit 75 asks
// chalkd to try again, and any other nonzero exit fails the effect.
type Readiness struct {
	Description    string   `json:"description"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

func (p Readiness) check() error {
	if strings.TrimSpace(p.Description) == "" {
		return errors.New("waitFor needs a description")
	}
	if len(p.Argv) == 0 || !command.InStore(p.Argv[0]) {
		return errors.New("waitFor needs an executable from the image's store")
	}
	if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 60 {
		return errors.New("waitFor.timeoutSeconds must be between 1 and 60")
	}
	return nil
}

type effectGroup struct {
	names []string
	after []string
}

// Schedule is the ordered batches of due effects. Effects that share an action are grouped so
// the action is performed once and every prerequisite of that action is met.
type Schedule struct {
	batches [][]effectGroup
}

// ScheduledEffect describes an effect and its effective prerequisites for previews.
type ScheduledEffect struct {
	Name       string
	After      []string
	SharedWith []string
}

// Order lists the effects in execution order for previews.
func (s Schedule) Order() []ScheduledEffect {
	var order []ScheduledEffect
	for _, batch := range s.batches {
		for _, group := range batch {
			for _, name := range group.names {
				step := ScheduledEffect{Name: name, After: group.after}
				for _, shared := range group.names {
					if shared != name {
						step.SharedWith = append(step.SharedWith, shared)
					}
				}
				order = append(order, step)
			}
		}
	}
	return order
}

func validateSchedule(defs map[string]Definition) error {
	names := slices.Sorted(maps.Keys(defs))
	for _, name := range names {
		seen := map[string]bool{}
		for _, dep := range defs[name].After {
			switch {
			case seen[dep]:
				return fmt.Errorf("effect %s repeats prerequisite %s", name, dep)
			case dep == name:
				return fmt.Errorf("effect %s depends on itself", name)
			}
			if _, ok := defs[dep]; !ok {
				return fmt.Errorf("effect %s has unknown prerequisite %s", name, dep)
			}
			seen[dep] = true
		}
	}
	_, err := PlanSchedule(defs, names)
	return err
}

// PlanSchedule orders only due effects. An edge to an unchanged effect does
// not run it. A consumer's readiness probe still runs if the consumer is due.
func PlanSchedule(defs map[string]Definition, due []string) (Schedule, error) {
	due = slices.Clone(due)
	slices.Sort(due)
	owners := map[string]string{}
	parents := map[string]string{}
	root := func(name string) string {
		for parents[name] != name {
			name = parents[name]
		}
		return name
	}
	for _, name := range due {
		parents[name] = name
		for _, key := range actionKeys(defs[name]) {
			if other, ok := owners[key]; ok {
				a, b := root(name), root(other)
				parents[max(a, b)] = min(a, b)
			} else {
				owners[key] = name
			}
		}
	}
	groups := map[string]effectGroup{}
	for _, name := range due {
		id := root(name)
		group := groups[id]
		group.names = append(group.names, name)
		for _, dep := range defs[name].After {
			if _, active := parents[dep]; !active {
				continue
			}
			owner := root(dep)
			if id == owner {
				return Schedule{}, fmt.Errorf("effect %s depends on %s, but they share a runtime action", name, dep)
			}
			if !slices.Contains(group.after, owner) {
				group.after = append(group.after, owner)
			}
		}
		groups[id] = group
	}
	var s Schedule
	for len(groups) > 0 {
		var batch []effectGroup
		for _, id := range slices.Sorted(maps.Keys(groups)) {
			group := groups[id]
			if !slices.ContainsFunc(group.after, func(dep string) bool {
				_, pending := groups[dep]
				return pending
			}) {
				batch = append(batch, group)
			}
		}
		if len(batch) == 0 {
			return Schedule{}, fmt.Errorf("runtime effect prerequisite cycle among %s", strings.Join(slices.Sorted(maps.Keys(groups)), ", "))
		}
		for _, group := range batch {
			delete(groups, group.names[0])
		}
		s.batches = append(s.batches, batch)
	}
	return s, nil
}

// actionKeys groups effects whose actions ActionsOf would coalesce.
func actionKeys(d Definition) []string {
	var keys []string
	for _, unit := range slices.Concat(d.Reload, d.Restart) {
		keys = append(keys, "unit:"+unit)
	}
	if len(d.Run) > 0 {
		keys = append(keys, "run:"+strings.Join(d.Run, "\x00"))
	}
	if d.Chalkd != "" {
		keys = append(keys, "chalkd:"+d.Chalkd)
	}
	return keys
}

// Batch is one group of effects executed using the usual systemd action order.
type Batch struct {
	Index   int
	Effects []string
}

// BatchProgress records work around a batch. Before can reuse an earlier
// completion. After is called only for batches this invocation actually ran.
type BatchProgress struct {
	Before func(Batch) (skip bool)
	After  func(Batch, bool)
}

// Apply executes the ordered schedule, preserving the usual systemd ordering
// within each batch. A systemctl error can cover several units in one command,
// so every effect that requested one of those units is treated as failed.
// When warnReadiness is set, a readiness failure is a warning and the effect
// can continue. The delivery context still bounds the work.
func (s Schedule) Apply(ctx context.Context, r tool.Runner, defs map[string]Definition, layer func(context.Context, string) error, warnReadiness func(error), progress *BatchProgress) ([]string, error) {
	failed := map[string]bool{}
	var errs []error
	var restarted []string
	for index, batch := range s.batches {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		step := Batch{Index: index}
		if progress != nil {
			for _, group := range batch {
				step.Effects = append(step.Effects, group.names...)
			}
			if progress.Before(step) {
				continue
			}
		}
		before := len(errs)
		var runnable []effectGroup
		var due []string
		for _, group := range batch {
			if ctx.Err() != nil {
				break
			}
			id := group.names[0]
			if i := slices.IndexFunc(group.after, func(dep string) bool { return failed[dep] }); i >= 0 {
				failed[id] = true
				errs = append(errs, fmt.Errorf("effects %s blocked by prerequisite %s", strings.Join(group.names, ", "), group.after[i]))
				continue
			}
			for _, name := range group.names {
				if p := defs[name].WaitFor; p != nil {
					if err := p.wait(ctx, r); err != nil {
						err = fmt.Errorf("effect %s: %w", name, err)
						if warnReadiness != nil && ctx.Err() == nil {
							warnReadiness(err)
							continue
						}
						failed[id] = true
						errs = append(errs, err)
						break
					}
				}
			}
			if !failed[id] {
				runnable = append(runnable, group)
				due = append(due, group.names...)
			}
		}
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		slices.Sort(due)
		a := ActionsOf(defs, due)
		units, err := Apply(ctx, r, a)
		restarted = append(restarted, units...)
		if err != nil {
			errs = append(errs, err)
		}
		for _, group := range runnable {
			id := group.names[0]
			actions := ActionsOf(defs, group.names)
			if actionFailed(err, actions) {
				failed[id] = true
			}
		}
		// Layer actions follow the batch's systemd jobs; a failed one blocks its dependents.
		for _, name := range a.Chalkd {
			if err := layer(ctx, name); err != nil {
				errs = append(errs, fmt.Errorf("layer action %s: %w", name, err))
				for _, group := range runnable {
					if slices.ContainsFunc(group.names, func(effect string) bool { return defs[effect].Chalkd == name }) {
						failed[group.names[0]] = true
					}
				}
			}
		}
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
		}
		if progress != nil {
			progress.After(step, before == len(errs) && ctx.Err() == nil)
		}
		if ctx.Err() != nil {
			break
		}
	}
	slices.Sort(restarted)
	return slices.Compact(restarted), errors.Join(errs...)
}

func actionFailed(err error, a Actions) bool {
	if err == nil {
		return false
	}
	failure, ok := errors.AsType[*ApplyError](err)
	if !ok {
		return true
	}
	b := failure.ByHand
	units := slices.Concat(b.Reload, b.Restart)
	return slices.ContainsFunc(slices.Concat(a.Reload, a.Restart), func(unit string) bool {
		return slices.Contains(units, unit)
	}) || slices.ContainsFunc(a.Run, func(run []string) bool {
		return slices.ContainsFunc(b.Run, func(other []string) bool { return slices.Equal(run, other) })
	})
}

// Readiness probes are tried every probeInterval, and an attempt that runs longer than probeAttempt
// is stopped and tried again.
const (
	probeInterval = 250 * time.Millisecond
	probeAttempt  = 2 * time.Second
)

func (p Readiness) wait(ctx context.Context, r tool.Runner) error {
	wait, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		if err := wait.Err(); err != nil {
			return fmt.Errorf("waiting for %s: %w", p.Description, err)
		}
		attempt, stop := context.WithTimeout(wait, probeAttempt)
		_, err := r.Exec(attempt, tool.Cmd{
			Name: p.Argv[0], Args: p.Argv[1:], Quiet: true, WaitDelay: 100 * time.Millisecond,
		})
		attemptErr := attempt.Err()
		stop()
		if err == nil && attemptErr == nil {
			return nil
		}
		if attemptErr == nil {
			if exit, ok := errors.AsType[*tool.Error](err); !ok {
				return fmt.Errorf("probe for %s could not run: %w", p.Description, err)
			} else if exit.Code != 75 {
				return fmt.Errorf("probe for %s exited with status %d", p.Description, exit.Code)
			}
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("waiting for %s: %w", p.Description, wait.Err())
		case <-ticker.C:
		}
	}
}
