package layer

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
)

// An error names its node once: one that names it already, also wrapped further, is printed as it
// is, and any other after the name, whatever its text starts with, with the name in place of
// <node>.
func TestNamed(t *testing.T) {
	named := &NodeError{Node: "r1", Err: errors.New("r1 runs config v2 on trial"), Named: true}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{named, "r1 runs config v2 on trial"},
		{&NodeError{Node: "r1", Err: errors.New("x"), Refused: true}, "r1: x"},
		{fmt.Errorf("wrapped: %w", named), "wrapped: r1 runs config v2 on trial"},
		{Named("r1", errors.New("the node refused it")), "r1: the node refused it"},
		{errors.New("r1: the config is refused: x"), "r1: r1: the config is refused: x"},
		{errors.New("dial tcp 10.0.0.11:4443: no route"), "r1: dial tcp 10.0.0.11:4443: no route"},
		{errors.New("roll back with chalkctl apply-config --rollback <node>"), "r1: roll back with chalkctl apply-config --rollback r1"},
	} {
		if got := Named("r1", tc.err).Error(); got != tc.want {
			t.Errorf("Named(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// Another node's error is named again.
	if got := Named("r2", Named("r1", errors.New("x"))).Error(); got != "r2: r1: x" {
		t.Errorf("Named for another node = %q", got)
	}
	if Named("r1", nil) != nil {
		t.Error("Named(nil) is not nil")
	}
	// The connect code stays reachable.
	if code := connect.CodeOf(Named("r1", connect.NewError(connect.CodeFailedPrecondition, errors.New("x")))); code != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want failed_precondition", code)
	}
}

// A message about the node that names it first drops the name an error about the same node puts
// first.
func TestUnnamed(t *testing.T) {
	inner := Named("cp2", errors.New("its control plane: kube-apiserver is not Ready"))
	got := (&NodeError{Node: "cp2", Err: fmt.Errorf("config v2 was not confirmed within its trial: %w", Unnamed("cp2", inner))}).Error()
	if want := "cp2: config v2 was not confirmed within its trial: its control plane: kube-apiserver is not Ready"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if other := Named("cp1", errors.New("x")); Unnamed("cp2", other) != other {
		t.Error("Unnamed dropped the name of another node")
	}
}
