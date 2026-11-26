package chalkctl

import (
	"context"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/golden"
)

// A node that cannot be reached is one line: the node, the address and where it came from, what
// failed, and the hint to check chalkd and the address.
func TestUnreachableNodeIsOneLine(t *testing.T) {
	ta := newTestApp(t)
	err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, "127.0.0.1:1"))
	if err == nil {
		t.Fatal("status of a node nothing listens for succeeded")
	}
	golden.Check(t, "testdata/unreachable.golden", "chalkctl: "+err.Error()+"\n")
}
