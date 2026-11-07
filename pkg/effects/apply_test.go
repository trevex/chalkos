package effects

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// Apply reloads what runs, restarts in the units' order, and restarts a failed unit after
// resetting it.
func TestApply(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: "systemctl show --property=ActiveState --value b.service", Out: "failed\n"},
		{Prefix: "systemctl show --property=ActiveState --value", Out: "active\n"},
		{Prefix: "systemctl show --property=Names,After", Out: "Names=r.service\nAfter=\n\nNames=b.service\nAfter=\n\nNames=a.service\nAfter=c.service\n\nNames=c.service\nAfter=\n"},
	}}
	a := ActionsOf(map[string]Definition{
		"one": {Kinds: []string{"K"}, Reload: []string{"r.service"}},
		"two": {Kinds: []string{"K"}, Restart: []string{"a.service", "b.service", "c.service"}, Run: "true now"},
	}, []string{"one", "two"})
	restarted, err := Apply(context.Background(), r, a)
	if err != nil || !reflect.DeepEqual(restarted, []string{"a.service", "b.service", "c.service"}) {
		t.Fatalf("Apply = %v, %v", restarted, err)
	}
	want := []string{
		"systemctl show --property=ActiveState --value r.service",
		"systemctl show --property=ActiveState --value a.service",
		"systemctl show --property=ActiveState --value b.service",
		"systemctl show --property=ActiveState --value c.service",
		"systemctl show --property=Names,After r.service b.service a.service c.service",
		"systemctl reload r.service",
		"true now",
		"systemctl reset-failed b.service",
		"systemctl restart b.service",
		"systemctl try-restart c.service a.service",
	}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Errorf("commands:\n got: %s\nwant: %s", strings.Join(r.Calls, "\n      "), strings.Join(want, "\n      "))
	}
}

// A restart that fails says what is left to do by hand, and why from the unit's hint.
func TestApplyFails(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "systemctl try-restart", Err: errors.New("exit status 1")}}}
	a := ActionsOf(map[string]Definition{
		"one": {Kinds: []string{"K"}, Restart: []string{"a.service"}, Hint: &Hint{Text: "a needs a disk"}},
	}, []string{"one"})
	_, err := Apply(context.Background(), r, a)
	ae, ok := errors.AsType[*ApplyError](err)
	if !ok || ae.Why != "a needs a disk" || !reflect.DeepEqual(ae.ByHand.Restart, []string{"a.service"}) ||
		err.Error() != "try-restart a.service: exit status 1; a needs a disk; the delivery is applied, so restart a.service by hand once that is fixed, or deliver it again" {
		t.Errorf("err = %v", err)
	}
}
