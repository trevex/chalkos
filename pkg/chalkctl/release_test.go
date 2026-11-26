package chalkctl

import (
	"context"
	"strings"
	"testing"
)

// A command that meets a node of another release warns once, though it asks the node's Info
// twice, and the next command warns again; a node of chalkctl's release draws no warning.
func TestReleaseWarning(t *testing.T) {
	ta := newTestApp(t)
	ta.release = "0.6.0"
	addr := cannedReleaseAt(t, ta, "0.5.0")
	const warning = "chalkctl: warning: n1 runs chalkd 0.5.0, this chalkctl is 0.6.0; use the chalkctl of the node's release\n"
	for range 2 {
		ta.stderr.Reset()
		if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(ta.stderr.String(), warning); got != 1 {
			t.Errorf("status warned %d times:\n%s", got, ta.stderr.String())
		}
	}
	ta.release = "0.5.0"
	ta.stderr.Reset()
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ta.stderr.String(), "warning") {
		t.Errorf("status of a node of chalkctl's release warns:\n%s", ta.stderr.String())
	}
}
