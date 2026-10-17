package chalkd

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// A fact a rule reads that cannot be loaded refuses the config, and changes nothing.
func TestApplyConfigRefusesWhenAFactCannotBeLoaded(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	write(t, s.Kubernetes.Paths.Share(), "{")
	r.calls = nil
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot check: fact share: ") || !strings.Contains(err.Error(), "share.json is not JSON") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}
