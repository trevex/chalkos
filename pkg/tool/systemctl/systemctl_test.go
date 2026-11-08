package systemctl

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

func TestShow(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{
		Prefix: "systemctl show --property=Names,After a.service b.service",
		Out:    "Names=a.service alias.service\nAfter=b.service sysinit.target\n\nNames=b.service\nAfter=\n",
	}}}
	got, err := Show(context.Background(), r, []string{"a.service", "b.service"}, "Names", "After")
	want := []map[string]string{
		{"Names": "a.service alias.service", "After": "b.service sysinit.target"},
		{"Names": "b.service", "After": ""},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Show = %v, %v, want %v", got, err, want)
	}
}

// Without units systemctl shows the manager's properties; a value may hold "=".
func TestShowManager(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "systemctl show", Out: "SystemState=running\nEnvironment=A=1\n"}}}
	got, err := Show(context.Background(), r, nil, "SystemState", "Environment")
	want := []map[string]string{{"SystemState": "running", "Environment": "A=1"}}
	if err != nil || !reflect.DeepEqual(got, want) || r.Calls[0] != "systemctl show --property=SystemState,Environment" {
		t.Errorf("Show = %v, %v, ran %v", got, err, r.Calls)
	}
}

func TestShowFails(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "systemctl", Err: errors.New("exit status 1")}}}
	if got, err := Show(context.Background(), r, []string{"a.service"}); err == nil || got != nil {
		t.Errorf("Show = %v, %v, want the error", got, err)
	}
}

func TestShowNothing(t *testing.T) {
	if got, err := Show(context.Background(), &tooltest.Runner{}, []string{"a.service"}, "Names"); err != nil || len(got) != 0 {
		t.Errorf("Show = %v, %v, want no units", got, err)
	}
}

// A failed unit's line may start with a mark of its state.
func TestFailedUnits(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{
		Prefix: "systemctl list-units --state=failed --plain --no-legend --no-pager",
		Out:    "broken.service loaded failed failed Broken\n● srv-extra.mount loaded failed failed Extra\n\n",
	}}}
	got, err := FailedUnits(context.Background(), r)
	if want := []string{"broken.service", "srv-extra.mount"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("FailedUnits = %v, %v, want %v", got, err, want)
	}
}

func TestUnits(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{
		Prefix: "systemctl list-units --all --plain --no-legend --no-pager a.service a@*.service",
		Out:    "a.service loaded active running A\n● a@x\\x2dy.service loaded failed failed A\n",
	}}}
	got, err := Units(context.Background(), r, "a.service", "a@*.service")
	if want := []string{"a.service", `a@x\x2dy.service`}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Units = %v, %v, want %v", got, err, want)
	}
}

func TestDependencies(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{
		Prefix: "systemctl list-dependencies --all --plain --no-legend --no-pager multi-user.target",
		Out:    "multi-user.target\n  chalkd.service\n    sysinit.target\n",
	}}}
	got, err := Dependencies(context.Background(), r, "multi-user.target")
	if want := []string{"multi-user.target", "chalkd.service", "sysinit.target"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Dependencies = %v, %v, want %v", got, err, want)
	}
}

func TestActiveState(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "systemctl show --property=ActiveState --value a.service", Out: "failed\n"}}}
	if got, err := ActiveState(context.Background(), r, "a.service"); err != nil || got != "failed" {
		t.Errorf("ActiveState = %q, %v", got, err)
	}
}

func TestIsActive(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "systemctl is-active a.service", Out: "active\n"},
		{Prefix: "systemctl is-active b.service", Out: "failed\n", Err: errors.New("exit status 3")},
	}}
	if state, err := IsActive(context.Background(), r, "a.service"); err != nil || state != "active" {
		t.Errorf("IsActive(a) = %q, %v", state, err)
	}
	if state, err := IsActive(context.Background(), r, "b.service"); err == nil || state != "failed" {
		t.Errorf("IsActive(b) = %q, %v", state, err)
	}
	if c, _ := r.Cmd("systemctl is-active a.service"); !c.Quiet {
		t.Error("is-active's standard error reached the console")
	}
}
