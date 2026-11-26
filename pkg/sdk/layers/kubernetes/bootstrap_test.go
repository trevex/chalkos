package kubernetes

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
)

// A bootstrap returns once the add-ons are applied, which the node's status says from then on, and
// a second one is refused.
func TestBootstrapTwiceIsRefused(t *testing.T) {
	conn := controlPlane(t, nil)
	if st, err := NodeStatus(context.Background(), conn); err != nil || st.AddonsApplied {
		t.Fatalf("status before the bootstrap: %v, %v", st, err)
	}
	resp, err := Bootstrap(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Applied != 19 {
		t.Errorf("applied %d objects, want 19", resp.Applied)
	}
	if st, err := NodeStatus(context.Background(), conn); err != nil || !st.AddonsApplied {
		t.Errorf("status after the bootstrap: %v, %v", st, err)
	}
	_, err = Bootstrap(context.Background(), conn)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bootstrapped already") {
		t.Errorf("second bootstrap: %v", err)
	}
}
