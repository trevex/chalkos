package router

import (
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/schema/schematest"
)

// A router image runs the validator router, which reads the documents of the router kinds and of
// Network, each kind once; an image without the Router kind runs none.
func TestValidators(t *testing.T) {
	if got := (Layer{}).Validators(schematest.Load(t, schematest.Builtin, schematest.Networkd)); got != nil {
		t.Errorf("an image without the router kinds runs %v", got)
	}
	got := (Layer{}).Validators(schematest.Load(t, schematest.Builtin, schematest.Networkd, schematest.Router))
	if len(got) != 1 || got[0].Name != "router" {
		t.Fatalf("validators = %v", got)
	}
	if inputs := slices.Sorted(slices.Values(got[0].Inputs)); !slices.Equal(inputs, slices.Sorted(slices.Values(derive.Kinds()))) {
		t.Errorf("inputs = %v, want %v", inputs, derive.Kinds())
	}
	if rs, err := got[0].Validate([]byte(`{"node": {"name": "r1"}, "documents": {}}`)); err != nil || len(rs) != 0 {
		t.Errorf("Validate of no documents = %v, %v", rs, err)
	}
	// An image without BGP declares none of its kinds, and the validator reads none of them.
	files := schematest.Files(t, schematest.Builtin, schematest.Networkd, schematest.Router)
	for _, kind := range []string{"BGP", "BGPPeer", "PrefixList", "CommunityList", "RoutingPolicy"} {
		delete(files, "router.chalkos.dev_v1alpha1_"+kind+".json")
	}
	noBGP, err := schema.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	got = (Layer{}).Validators(noBGP)
	if len(got) != 1 || slices.Contains(got[0].Inputs, "BGPPeer") || !slices.Contains(got[0].Inputs, "VRF") {
		t.Errorf("validators of an image without BGP = %v", got)
	}
}
