package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// The VRF kind's rules accept an edge with two VRFs and refuse the name default, a name past 15
// characters, a table of the kernel's, two VRFs of one table, an interface in two VRFs, a
// management interface in a VRF, a node whose node API answers on every interface but whose
// Network documents all name links of VRFs, and a NetDev of a VRF's name. Names and globs compare
// as written.
func TestVRFRules(t *testing.T) {
	set, doc := routerRules(t)
	networks := []config.Document{
		network(t, "10-eth0", `{"name": "eth0"}`),
		network(t, "20-eth1", `{"name": "eth1"}`),
		network(t, "20-eth2", `{"name": "eth2"}`),
		network(t, "30-swp", `{"name": "swp1 swp2"}`),
	}
	router := doc("Router", "router", `{"management": {"interfaces": ["eth0"]}}`)
	tenant := doc("VRF", "tenant", `{"table": 100, "interfaces": ["eth2", "swp*"]}`)
	internet := doc("VRF", "internet", `{"table": 200, "interfaces": ["eth1"]}`)
	edge := append([]config.Document{router, tenant, internet}, networks...)
	with := func(docs []config.Document, more ...config.Document) []config.Document {
		return append(slices.Clone(docs), more...)
	}
	without := func(docs []config.Document, kind, name string) []config.Document {
		return slices.DeleteFunc(slices.Clone(docs), func(d config.Document) bool { return d.Kind == kind && d.Name == name })
	}
	// Without the Router, eth0's Network and that of the swp links, whose names the glob swp*
	// matches only as written, every named link is a member as written.
	allInVRFs := without(without(without(edge, "Router", "router"), "Network", "10-eth0"), "Network", "30-swp")
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an edge with two VRFs", edge, nil},
		{"a link a glob holds counts as written", without(without(edge, "Router", "router"), "Network", "10-eth0"), nil},
		{"no VRF", append([]config.Document{router}, networks...), nil},
		{"the node API on every interface and a link outside every VRF", without(edge, "Router", "router"), nil},
		{"globs compare as written", with(without(edge, "VRF", "internet"), doc("VRF", "lab", `{"table": 300, "interfaces": ["swp1"]}`)), nil},
		{"a VRF named default", with(edge, doc("VRF", "default", `{"table": 300}`)),
			[]string{"VRF default: a VRF is named as its device, in 15 characters at most, and default names the VRF of every link no VRF holds"}},
		{"a name of 16 characters", with(edge, doc("VRF", "tenant-12345678x", `{"table": 300}`)),
			[]string{"VRF tenant-12345678x: a VRF is named as its device, in 15 characters at most, and default names the VRF of every link no VRF holds"}},
		{"the main table", with(edge, doc("VRF", "lab", `{"table": 254}`)),
			[]string{"VRF lab uses table 253, 254 or 255, which the kernel keeps for the default VRF; give it another number"}},
		{"two VRFs of one table", with(edge, doc("VRF", "lab", `{"table": 100}`)),
			[]string{"more than one VRF uses table 100; give each VRF a table of its own"}},
		{"an interface in two VRFs", with(edge, doc("VRF", "lab", `{"table": 300, "interfaces": ["eth2"]}`)),
			[]string{"the interface eth2 is in more than one VRF, or twice in one; give each interface one VRF"}},
		{"a management interface in a VRF", with(without(edge, "Router", "router"), doc("Router", "router", `{"management": {"interfaces": ["eth0", "eth1"]}}`)),
			[]string{"the management interface eth1 is in a VRF, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF"}},
		{"every link in a VRF without a Router", allInVRFs,
			[]string{"every link the Network documents name is in a VRF, and chalkd answers in the default VRF alone, on every interface while Router.spec.management.interfaces is null; keep the link you reach the router through out of every VRF"}},
		{"every link in a VRF with the node API on every interface", with(allInVRFs, doc("Router", "router", `{}`)),
			[]string{"every link the Network documents name is in a VRF, and chalkd answers in the default VRF alone, on every interface while Router.spec.management.interfaces is null; keep the link you reach the router through out of every VRF"}},
		{"a NetDev of a VRF's name", with(edge, builtinDoc(t, config.NetworkdAPI, "NetDev", "20-tenant", `{"netDev": {"kind": "dummy", "name": "tenant"}}`)),
			[]string{"the VRF tenant takes the name of a device a NetDev document creates; rename the VRF or the NetDev"}},
		{"a NetDev of another name", with(edge, builtinDoc(t, config.NetworkdAPI, "NetDev", "20-edge0", `{"netDev": {"kind": "dummy", "name": "edge0"}}`)), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// The VRF kind's schema takes tables from 1 to 2^32 - 1 and interfaces by name or glob.
func TestVRFSchema(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		spec string
		ok   bool
	}{
		{`{"table": 100}`, true},
		{`{"table": 4294967295, "interfaces": ["eth1", "swp*"]}`, true},
		{`{"table": 0}`, false},
		{`{"table": 4294967296}`, false},
		{`{"interfaces": ["eth1"]}`, false},
		{`{"table": 100, "interfaces": ["eth 1"]}`, false},
	} {
		d := builtinDoc(t, config.RouterAPI, "VRF", "tenant", tc.spec)
		if err := s.Validate(d.Object); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want accepted %v", tc.spec, err, tc.ok)
		}
	}
}
