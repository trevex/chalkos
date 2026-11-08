// Package systemctl asks systemd about units with systemctl, run through a tool.Runner, and reads
// its answers.
package systemctl

import (
	"context"
	"strings"

	"github.com/trevex/chalkos/pkg/tool"
)

// Show returns the properties props of units, each unit's in the order given, or of the manager
// without units. A unit given by a glob shows the units it matches instead, so the caller checks
// that each holds the unit it asked for, such as by its Names.
func Show(ctx context.Context, r tool.Runner, units []string, props ...string) ([]map[string]string, error) {
	args := []string{"show"}
	if len(props) > 0 {
		args = append(args, "--property="+strings.Join(props, ","))
	}
	out, err := tool.Run(ctx, r, "systemctl", append(args, units...)...)
	if err != nil {
		return nil, err
	}
	return parseShow(out), nil
}

// parseShow reads systemctl show's output: each unit's properties as lines of name=value, a blank
// line between units.
func parseShow(out []byte) []map[string]string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil
	}
	var units []map[string]string
	for block := range strings.SplitSeq(text, "\n\n") {
		props := map[string]string{}
		for line := range strings.SplitSeq(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				props[k] = v
			}
		}
		units = append(units, props)
	}
	return units
}

// ActiveState returns unit's ActiveState, such as "active", "inactive" or "failed".
func ActiveState(ctx context.Context, r tool.Runner, unit string) (string, error) {
	out, err := tool.Run(ctx, r, "systemctl", "show", "--property=ActiveState", "--value", unit)
	return strings.TrimSpace(string(out)), err
}

// IsActive asks whether unit is active, as systemctl is-active tells by its exit status, which
// counts a unit that reloads as active too: err is nil only for an active unit. state is what
// systemctl printed, such as "failed" for a failed unit; "" when it printed nothing, as when it
// did not run. Its standard error goes into err alone, for a question asked often.
func IsActive(ctx context.Context, r tool.Runner, unit string) (state string, err error) {
	out, err := r.Exec(ctx, tool.Cmd{Name: "systemctl", Args: []string{"is-active", unit}, Quiet: true})
	return strings.TrimSpace(string(out)), err
}

// FailedUnits returns the names of the units that failed.
func FailedUnits(ctx context.Context, r tool.Runner) ([]string, error) {
	out, err := tool.Run(ctx, r, "systemctl", "list-units", "--state=failed", "--plain", "--no-legend", "--no-pager")
	if err != nil {
		return nil, err
	}
	return unitNames(out), nil
}

// Units returns the names of the units systemd holds whose names match one of patterns, such as
// "unbound@*.service", whether they are active or not.
func Units(ctx context.Context, r tool.Runner, patterns ...string) ([]string, error) {
	out, err := tool.Run(ctx, r, "systemctl", append([]string{"list-units", "--all", "--plain", "--no-legend", "--no-pager"}, patterns...)...)
	if err != nil {
		return nil, err
	}
	return unitNames(out), nil
}

// Dependencies returns the names of units and of all the units they depend on, recursively.
func Dependencies(ctx context.Context, r tool.Runner, units ...string) ([]string, error) {
	out, err := tool.Run(ctx, r, "systemctl", append([]string{"list-dependencies", "--all", "--plain", "--no-legend", "--no-pager"}, units...)...)
	if err != nil {
		return nil, err
	}
	return unitNames(out), nil
}

// unitNames reads the unit names of systemctl's list lines: the first word naming a unit, past any
// mark of its state, such as the ● some systemd versions print before a failed unit.
func unitNames(out []byte) []string {
	var names []string
	for line := range strings.Lines(string(out)) {
		for _, f := range strings.Fields(line) {
			if strings.Contains(f, ".") {
				names = append(names, f)
				break
			}
		}
	}
	return names
}
