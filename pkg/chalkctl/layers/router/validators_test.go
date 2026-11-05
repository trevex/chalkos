package router

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
)

// schemas are the schemas of the fixture directories, as one image ships them.
func schemas(t *testing.T, dirs ...string) *schema.Schemas {
	t.Helper()
	out := t.TempDir()
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, e.Name()), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	s, err := schema.LoadSchemas(out)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	builtinSchemas  = "../../../../test/fixtures/kinds/builtin"
	networkdSchemas = "../../../../test/fixtures/config/networkd/schemas"
	routerSchemas   = "../../../../test/fixtures/kinds/router"
)

// A router image runs the validator router, which reads the documents of the router kinds and of
// Network, each kind once; an image without the Router kind runs none.
func TestValidators(t *testing.T) {
	if got := New().Validators(schemas(t, builtinSchemas, networkdSchemas)); got != nil {
		t.Errorf("an image without the router kinds runs %v", got)
	}
	got := New().Validators(schemas(t, builtinSchemas, networkdSchemas, routerSchemas))
	if len(got) != 1 || got[0].Name != "router" {
		t.Fatalf("validators = %v", got)
	}
	if inputs := slices.Sorted(slices.Values(got[0].Inputs)); !slices.Equal(inputs, slices.Sorted(slices.Values(derive.Kinds))) {
		t.Errorf("inputs = %v, want %v", inputs, derive.Kinds)
	}
	if rs, err := got[0].Validate([]byte("[]")); err != nil || len(rs) != 0 {
		t.Errorf("Validate([]) = %v, %v", rs, err)
	}
	// A variant without BGP declares none of its kinds, and the validator reads none of them.
	noBGP := t.TempDir()
	entries, err := os.ReadDir(routerSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if kind := e.Name(); !slices.Contains([]string{"BGP", "BGPPeer", "PrefixList", "CommunityList", "RoutingPolicy"}, kind[len("router.chalkos.dev_v1alpha1_"):len(kind)-len(".json")]) {
			data, err := os.ReadFile(filepath.Join(routerSchemas, kind))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(noBGP, kind), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got = New().Validators(schemas(t, builtinSchemas, networkdSchemas, noBGP))
	if len(got) != 1 || slices.Contains(got[0].Inputs, "BGPPeer") || !slices.Contains(got[0].Inputs, "VRF") {
		t.Errorf("validators of an image without BGP = %v", got)
	}
}
