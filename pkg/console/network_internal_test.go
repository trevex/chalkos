package console

import (
	"slices"
	"testing"
)

// A multipath default route lists each path.
func TestDefaultRoutesMultipath(t *testing.T) {
	routes, err := parseRoutes([]byte(`[{"dst":"default","protocol":"bgp","nexthops":[{"gateway":"192.0.2.1","dev":"enp1s0","weight":1},{"gateway":"192.0.2.9","dev":"enp2s0","weight":2}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"via 192.0.2.1 dev enp1s0 weight 1 (bgp)", "via 192.0.2.9 dev enp2s0 weight 2 (bgp)"}
	if got := defaultRoutes(routes[0]); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
