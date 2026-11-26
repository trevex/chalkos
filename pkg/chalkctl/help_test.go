package chalkctl

import (
	"context"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/golden"
)

// TestRootHelp holds chalkctl --help: the commands in groups, set up and operate apart from each
// layer's.
func TestRootHelp(t *testing.T) {
	ta := newTestApp(t)
	if got := ta.execute(context.Background(), "--help"); got != 0 {
		t.Fatalf("chalkctl --help exits with %d:\n%s", got, ta.stderr.String())
	}
	golden.Check(t, "testdata/help.golden", ta.stdout.String())
}

// A mistyped command gets cobra's suggestion.
func TestUnknownCommandSuggests(t *testing.T) {
	ta := newTestApp(t)
	if got := ta.execute(context.Background(), "stauts", "cp1"); got != 2 {
		t.Fatalf("chalkctl stauts exits with %d", got)
	}
	if want := "chalkctl: usage: unknown command \"stauts\" for \"chalkctl\"\n\nDid you mean this?\n\tstatus\n"; !strings.HasPrefix(ta.stderr.String(), want) {
		t.Errorf("chalkctl stauts prints %q, want it to start with %q", ta.stderr.String(), want)
	}
}

// The help of the longest commands shows their usage within a screen; the reference page holds
// the rest of their description.
func TestHelpFitsAScreen(t *testing.T) {
	for _, args := range [][]string{{"apply"}, {"config", "apply"}, {"upgrade"}, {"secureboot", "enroll"}, {"node", "reboot"}} {
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), append(args, "--help")...); got != 0 {
			t.Fatalf("chalkctl %v --help exits with %d", args, got)
		}
		lines := strings.Split(ta.stdout.String(), "\n")
		usage := -1
		for i, line := range lines {
			if line == "Usage:" {
				usage = i
				break
			}
		}
		if usage < 0 || usage > 40 {
			t.Errorf("chalkctl %v --help shows Usage at line %d, want within 40:\n%s", args, usage+1, ta.stdout.String())
		}
	}
}

// A node stays held in the words of the layers that hold it, or by its layers when one gives none.
func TestHeldNote(t *testing.T) {
	member := func(held string) upgradeRole { return upgradeRole{role: layer.UpgradeRole{Member: true, Held: held}} }
	for _, tc := range []struct {
		roles upgradeRoles
		want  string
	}{
		{upgradeRoles{member("cordoned")}, "it stays cordoned"},
		{upgradeRoles{member("cordoned"), member("cordoned")}, "it stays cordoned"},
		{upgradeRoles{member("cordoned"), member("fenced")}, "it stays cordoned and fenced"},
		{upgradeRoles{member("cordoned"), member("")}, "it stays held by its layers"},
		{upgradeRoles{member("cordoned"), {role: layer.UpgradeRole{Coordinator: true}}}, "it stays cordoned"},
	} {
		if got := tc.roles.heldNote(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.roles, got, tc.want)
		}
	}
}
