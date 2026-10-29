package kubernetes

import (
	"errors"
	"fmt"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
)

// fakeUpgrading is a run whose ViaCoordinators fails with err.
type fakeUpgrading struct {
	layer.Upgrading
	err error
}

func (u fakeUpgrading) ViaCoordinators(string, func(error) bool, func(*client.Conn) error) error {
	return u.err
}

// viaControlPlanes names the control planes when the cluster has no coordinator, and passes any
// other error on as it is.
func TestViaControlPlanes(t *testing.T) {
	call := func(err error) error {
		return viaControlPlanes(fakeUpgrading{err: err}, "w1", tryNextControlPlane, func(*client.Conn) error { return nil })
	}
	none := fmt.Errorf("drain w1: %w", layer.ErrNoCoordinator)
	if err := call(none); err == nil || err.Error() != "the cluster has no control-plane node to ask; pass --inventory" {
		t.Errorf("without a coordinator: %v", err)
	}
	other := errors.New("connection refused")
	if err := call(other); err != other {
		t.Errorf("another error = %v, want it as it is", err)
	}
	if err := call(nil); err != nil {
		t.Errorf("a call that succeeds = %v", err)
	}
}
