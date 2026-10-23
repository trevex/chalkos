package ociimage

import (
	"testing"
	"time"
)

// SetStallTimeout makes a layer's fetch wait d for data from the registry for the rest of the test.
func SetStallTimeout(t *testing.T, d time.Duration) {
	old := stallTimeout
	stallTimeout = d
	t.Cleanup(func() { stallTimeout = old })
}

// SetSourceTimeout makes a fetch of the manifest and the header wait d for one source for the rest
// of the test.
func SetSourceTimeout(t *testing.T, d time.Duration) {
	old := sourceTimeout
	sourceTimeout = d
	t.Cleanup(func() { sourceTimeout = old })
}

// SetFastRetries makes a source's fetch retry after a millisecond for the rest of the test.
func SetFastRetries(t *testing.T) {
	old := retryBackoff
	retryBackoff.Duration = time.Millisecond
	t.Cleanup(func() { retryBackoff = old })
}

// Passable is passable.
var Passable = passable
