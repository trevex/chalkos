package effects

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

var readyProbe = &Readiness{
	Description:    "a configured route",
	Argv:           []string{"/nix/store/demo/bin/check-route", "fabric"},
	TimeoutSeconds: 1,
}

func TestScheduleRunsReadinessAfterFailedUnitRecovery(t *testing.T) {
	defs := map[string]Definition{
		"vrf":      {Reload: []string{"chalkos-vrf.service"}},
		"networkd": {After: []string{"vrf"}, Reload: []string{"networkd.service"}},
		"resolver": {After: []string{"networkd"}, WaitFor: readyProbe, Reload: []string{"resolver.service"}},
	}
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "systemctl show --property=ActiveState --value chalkos-vrf.service", Out: "failed\n"},
		{Prefix: "/nix/store/demo/bin/check-route", Err: &tool.Error{Code: 75}, Times: 1},
	}}
	s, err := PlanSchedule(defs, []string{"resolver", "networkd", "vrf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), r, defs, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl show --property=ActiveState --value chalkos-vrf.service",
		"systemctl reset-failed chalkos-vrf.service",
		"systemctl restart chalkos-vrf.service",
		"systemctl show --property=ActiveState --value networkd.service",
		"systemctl reload networkd.service",
		"/nix/store/demo/bin/check-route fabric",
		"/nix/store/demo/bin/check-route fabric",
		"systemctl show --property=ActiveState --value resolver.service",
		"systemctl reload resolver.service",
	}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Fatalf("commands = %v, want %v", r.Calls, want)
	}
}

func TestScheduleBlocksDependentsAndKeepsIndependentEffects(t *testing.T) {
	defs := map[string]Definition{
		"networkd":  {Restart: []string{"networkd.service"}},
		"resolver":  {After: []string{"networkd"}, WaitFor: readyProbe, Reload: []string{"resolver.service"}},
		"app":       {After: []string{"resolver"}, Run: "update-app"},
		"telemetry": {Run: "configure-telemetry"},
	}
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "systemctl try-restart networkd.service", Err: errors.New("failed")},
	}}
	s, err := PlanSchedule(defs, []string{"resolver", "networkd", "app", "telemetry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), r, defs, nil, func(err error) {
		t.Errorf("force overrode a failed prerequisite: %v", err)
	}, nil); err == nil ||
		!strings.Contains(err.Error(), "resolver blocked by prerequisite networkd") ||
		!strings.Contains(err.Error(), "app blocked by prerequisite resolver") {
		t.Fatalf("Apply error = %v", err)
	}
	if !slices.Contains(r.Calls, "configure-telemetry") ||
		slices.Contains(r.Calls, "systemctl reload resolver.service") ||
		slices.Contains(r.Calls, "/nix/store/demo/bin/check-route fabric") ||
		slices.Contains(r.Calls, "update-app") {
		t.Fatalf("commands = %v", r.Calls)
	}
}

func TestScheduleWaitsForReadinessWithUnchangedPrerequisite(t *testing.T) {
	defs := map[string]Definition{
		"networkd": {Reload: []string{"networkd.service"}},
		"resolver": {After: []string{"networkd"}, WaitFor: readyProbe, Reload: []string{"resolver.service"}},
	}
	s, err := PlanSchedule(defs, []string{"resolver"})
	if err != nil {
		t.Fatal(err)
	}
	r := &tooltest.Runner{}
	if _, err := s.Apply(t.Context(), r, defs, nil, nil, nil); err != nil || len(r.Calls) != 3 ||
		r.Calls[0] != "/nix/store/demo/bin/check-route fabric" || r.Calls[2] != "systemctl reload resolver.service" {
		t.Fatalf("Apply = %v, commands = %v", err, r.Calls)
	}

	r = &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "/nix/store/demo/bin/check-route", Err: &tool.Error{Code: 75}}}}
	if _, err := s.Apply(t.Context(), r, defs, nil, nil, nil); !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "waiting for a configured route") {
		t.Fatalf("timeout = %v", err)
	}
	if len(r.Calls) < 2 || slices.Contains(r.Calls, "systemctl reload resolver.service") {
		t.Fatalf("commands after timeout = %v", r.Calls)
	}

	r = &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "/nix/store/demo/bin/check-route", Err: &tool.Error{Code: 1, Stderr: "do not disclose"}}}}
	if _, err := s.Apply(t.Context(), r, defs, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "exited with status 1") ||
		strings.Contains(err.Error(), "do not disclose") || len(r.Calls) != 1 {
		t.Fatalf("fatal probe = %v, commands = %v", err, r.Calls)
	}
}

func TestScheduleMergesSharedActionsBeforeOrdering(t *testing.T) {
	defs := map[string]Definition{
		"networkd": {Run: "configure-network"},
		"reload":   {After: []string{"networkd"}, Reload: []string{"resolver.service"}},
		"restart":  {Start: true, Restart: []string{"resolver.service"}},
	}
	s, err := PlanSchedule(defs, []string{"restart", "reload", "networkd"})
	if err != nil {
		t.Fatal(err)
	}
	r := &tooltest.Runner{}
	if _, err := s.Apply(t.Context(), r, defs, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"configure-network", "systemctl show --property=ActiveState --value resolver.service", "systemctl restart resolver.service"}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Fatalf("commands = %v, want %v", r.Calls, want)
	}
}

func TestScheduleForcesReadinessTimeoutButStopsOnCancellation(t *testing.T) {
	defs := map[string]Definition{"resolver": {WaitFor: readyProbe, Run: "reload-resolver"}}
	s, err := PlanSchedule(defs, []string{"resolver"})
	if err != nil {
		t.Fatal(err)
	}
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "/nix/store/demo/bin/check-route", Err: &tool.Error{Code: 75}},
	}}
	var warning error
	if _, err := s.Apply(t.Context(), r, defs, nil, func(err error) { warning = err }, nil); err != nil ||
		!errors.Is(warning, context.DeadlineExceeded) || !slices.Contains(r.Calls, "reload-resolver") {
		t.Fatalf("force = %v, warning = %v, commands = %v", err, warning, r.Calls)
	}

	r.ResetCalls()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.Before = func(string) { cancel() }
	if _, err := s.Apply(ctx, r, defs, nil, func(err error) { t.Errorf("overrode cancellation: %v", err) }, nil); !errors.Is(err, context.Canceled) ||
		slices.Contains(r.Calls, "reload-resolver") {
		t.Fatalf("canceled force = %v, commands = %v", err, r.Calls)
	}
}

func TestScheduleRejectsInvalidPrerequisites(t *testing.T) {
	for _, test := range []struct {
		name string
		defs map[string]Definition
		text string
	}{
		{"unknown", map[string]Definition{"a": {After: []string{"b"}}}, "unknown prerequisite b"},
		{"self", map[string]Definition{"a": {After: []string{"a"}}}, "depends on itself"},
		{"duplicate", map[string]Definition{"a": {After: []string{"b", "b"}}, "b": {}}, "repeats prerequisite b"},
		{"cycle", map[string]Definition{"a": {After: []string{"b"}}, "b": {After: []string{"a"}}}, "prerequisite cycle"},
		{"shared action", map[string]Definition{
			"a": {After: []string{"b"}, Reload: []string{"r.service"}}, "b": {Restart: []string{"r.service"}},
		}, "share a runtime action"},
		{"merged cycle", map[string]Definition{
			"a": {After: []string{"b"}, Reload: []string{"r.service"}}, "b": {After: []string{"c"}},
			"c": {Restart: []string{"r.service"}},
		}, "prerequisite cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSchedule(test.defs); err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("validation = %v, want %q", err, test.text)
			}
		})
	}
}

func TestScheduleRunsEffectsWithoutPrerequisitesInOneBatch(t *testing.T) {
	defs := map[string]Definition{
		"resolver": {Reload: []string{"resolver.service"}},
		"layer":    {Chalkd: "refresh"},
	}
	s, err := PlanSchedule(defs, []string{"resolver", "layer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.batches) != 1 || len(s.Order()) != 2 {
		t.Fatalf("schedule = %+v", s.batches)
	}
	r := &tooltest.Runner{}
	var ran []string
	layer := func(_ context.Context, name string) error {
		ran = append(ran, name)
		return errors.New("refused")
	}
	if _, err := s.Apply(t.Context(), r, defs, layer, nil, nil); err == nil || !strings.Contains(err.Error(), "layer action refresh: refused") {
		t.Fatalf("Apply = %v", err)
	}
	if !slices.Equal(ran, []string{"refresh"}) || !slices.Contains(r.Calls, "systemctl reload resolver.service") {
		t.Fatalf("layer actions = %v, commands = %v", ran, r.Calls)
	}
}

func TestReadinessRespectsEarlierDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "/nix/store/demo/bin/check-route", Err: &tool.Error{Code: 75}}}}
	if err := readyProbe.wait(ctx, r); !errors.Is(err, context.DeadlineExceeded) ||
		len(r.Calls) != 1 {
		t.Fatalf("wait = %v, commands = %v", err, r.Calls)
	}
}
