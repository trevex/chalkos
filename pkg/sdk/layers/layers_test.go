package layers

import (
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/layers"
)

// All names each layer of chalkos once, in the order of their names.
func TestAllNamesEveryLayer(t *testing.T) {
	var names []string
	for _, l := range All() {
		names = append(names, l.Name())
	}
	if want := []string{layers.Kubernetes, layers.Router}; !slices.Equal(names, want) {
		t.Errorf("All names %v, want %v", names, want)
	}
}
