package chalkd

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/rules"
)

// A refusal names the most a change takes first, and each rule's message.
func TestRefusal(t *testing.T) {
	reset := rules.Failure{Kind: "Volume", Takes: rules.Reset, Message: "Volume extra: format xfs instead of ext4; reset it"}
	reinstall := rules.Failure{Kind: "Volume", Takes: rules.Reinstall, Message: "Volume data: removed; only a reinstall removes it"}
	refuse := rules.Failure{Kind: "Kubernetes", Document: "kubernetes", Path: []string{"spec", "ipFamilies"}, Takes: rules.Refuse, Message: "fixed"}
	for _, tc := range []struct {
		failures []rules.Failure
		want     string
	}{
		{[]rules.Failure{reset}, "the config takes chalkctl storage reset: Volume extra: format xfs instead of ext4; reset it"},
		{[]rules.Failure{reset, reinstall}, "the config takes a reinstall of n1: Volume extra: format xfs instead of ext4; reset it; Volume data: removed; only a reinstall removes it"},
		{[]rules.Failure{reinstall, refuse}, `the config is refused: Volume data: removed; only a reinstall removes it; Kubernetes "kubernetes" spec.ipFamilies: fixed`},
	} {
		if got := refusal(tc.failures, "n1"); got != tc.want {
			t.Errorf("refusal = %q, want %q", got, tc.want)
		}
	}
}

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
