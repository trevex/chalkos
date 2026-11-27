package kubernetes

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
)

// A bootstrap returns once the add-ons are applied, which the node's status says from then on, and
// a second one is refused. A bootstrap whose context has no deadline reaches the node without one.
func TestBootstrapTwiceIsRefused(t *testing.T) {
	var timeout atomic.Pointer[string]
	conn := controlPlane(t, nil, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == kubernetesv1connect.KubernetesServiceBootstrapProcedure {
				v := r.Header.Get("Connect-Timeout-Ms")
				timeout.CompareAndSwap(nil, &v)
			}
			h.ServeHTTP(w, r)
		})
	})
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
	if v := timeout.Load(); v == nil || *v != "" {
		t.Errorf("the bootstrap reached the node with a timeout, want none")
	}
	if st, err := NodeStatus(context.Background(), conn); err != nil || !st.AddonsApplied {
		t.Errorf("status after the bootstrap: %v, %v", st, err)
	}
	_, err = Bootstrap(context.Background(), conn)
	if !BootstrappedAlready(err) || !strings.Contains(err.Error(), "bootstrapped already") {
		t.Errorf("second bootstrap: %v, want the reason BOOTSTRAPPED_ALREADY", err)
	}
	if BootstrappedAlready(connect.NewError(connect.CodeFailedPrecondition, "the node is bootstrapped already")) {
		t.Error("a message without the reason counts as bootstrapped already")
	}
}
