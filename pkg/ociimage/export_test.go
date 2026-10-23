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
