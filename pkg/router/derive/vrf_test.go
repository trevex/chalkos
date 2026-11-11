package derive

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// The links of the Network documents are in the VRF whose document or whose Network's own VRF=
// holds them; the Networks are in the VRF of the links they are first to match.
func TestVRFLinks(t *testing.T) {
	byKind := map[string]any{"Network": []any{
		derivetest.Doc(t, "Network", "10-swp", `{"match": {"name": "swp1 swp2"}}`),
		derivetest.Doc(t, "Network", "20-swp", `{"match": {"name": "swp*"}}`),
		derivetest.Doc(t, "Network", "30-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "blue"}}`),
		derivetest.Doc(t, "Network", "40-eth0", `{"match": {"name": "eth0"}}`),
	}}
	got, err := vrfLinks([]Named[VRFSpec]{{"tenant", VRFSpec{Table: 100, Interfaces: []string{"swp*"}}}}, networkLinks(byKind), nil, byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"swp1": "tenant", "swp2": "tenant", "eth1": "blue", "eth0": DefaultVRF} {
		if vrf, err := got.LinkVRF(link); err != nil || vrf != want {
			t.Errorf("linkVRF(%s) = %q, %v, want %q", link, vrf, err, want)
		}
	}
	if want := map[string]string{"10-swp": "tenant", "20-swp": "tenant", "30-eth1": "blue"}; !reflect.DeepEqual(got.Networks, want) {
		t.Errorf("networks = %v, want %v", got.Networks, want)
	}
}

// networkd puts a link in the VRF of the Network it picks, which a Network whose other [Match]
// keys may not match leaves open unless a Network that matches the link by Name= alone follows.
func TestLinkVRFNarrowed(t *testing.T) {
	mac := `, "macAddress": "02:00:00:00:00:01"`
	byKind := map[string]any{"Network": []any{
		derivetest.Doc(t, "Network", "10-swp", `{"match": {"name": "swp1 swp2"`+mac+`}}`),
		derivetest.Doc(t, "Network", "20-swp2", `{"match": {"name": "swp2"}}`),
		derivetest.Doc(t, "Network", "30-eth", `{"match": {"name": "eth0"`+mac+`}}`),
	}}
	m, err := vrfLinks([]Named[VRFSpec]{{"tenant", VRFSpec{Table: 100, Interfaces: []string{"swp*"}}}}, networkLinks(byKind), nil, byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"swp2": "tenant", "eth0": DefaultVRF, "lo": DefaultVRF} {
		if got, err := m.LinkVRF(link); err != nil || got != want {
			t.Errorf("linkVRF(%s) = %q, %v, want %q", link, got, err, want)
		}
	}
	want := "networkd puts swp1 in VRF tenant only if the other [Match] keys of Network 10-swp match it"
	if _, err := m.LinkVRF("swp1"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("linkVRF(swp1): err = %v, want %q", err, want)
	}
}

// Other [Match] keys narrow a Network unless an empty value resets them, as systemd reads it.
func TestOtherMatchKeys(t *testing.T) {
	mac := "02:00:00:00:00:01"
	for _, c := range []struct {
		match any
		want  bool
	}{
		{map[string]any{"name": "eth1"}, false},
		{map[string]any{"name": "eth1", "macAddress": mac}, true},
		{map[string]any{"name": "eth1", "macAddress": ""}, false},
		{map[string]any{"name": "eth1", "macAddress": " \t"}, false},
		{map[string]any{"name": "eth1", "macAddress": []any{}}, false},
		{map[string]any{"name": "eth1", "macAddress": []any{mac, ""}}, false},
		{map[string]any{"name": "eth1", "macAddress": []any{"", mac}}, true},
		{[]any{map[string]any{"macAddress": mac}, map[string]any{"macAddress": ""}}, false},
		{[]any{map[string]any{"macAddress": ""}, map[string]any{"driver": "e1000"}}, true},
	} {
		if got := otherMatchKeys(map[string]any{"match": c.match}); got != c.want {
			t.Errorf("otherMatchKeys(%v) = %v, want %v", c.match, got, c.want)
		}
	}
}

// The VRFs' links refuse what networkd would apply otherwise than the documents say: a link two
// VRFs hold, a Network of links in and out of a VRF, a Network that keeps another's VRF= from its
// links, a VRF device a Network matches first and a VRF named as a link. They guard, with an
// internal error, names and tables networkd would not load and values that would add to a unit.
func TestVRFLinkRefusals(t *testing.T) {
	vrf := func(name, spec string) any { return derivetest.RouterDoc(t, "VRF", name, spec) }
	net := func(name, spec string) any { return derivetest.Doc(t, "Network", name, spec) }
	link := func(name string) any { return net("20-"+name, `{"match": {"name": "`+name+`"}}`) }
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a member no Network names": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth9"]}`)}, "Network": {link("eth1")}},
			"VRF/tenant: spec.interfaces[0]: VRF tenant names the interface eth9, which no Network document's [Match] Name= matches without other [Match] keys; VRF= reaches a link through its Network document, so give it one"},
		"a link a glob puts in a second VRF": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth*"]}`), vrf("b", `{"table": 200, "interfaces": ["eth1"]}`)}, "Network": {link("eth1")}},
			"VRF/b: spec.interfaces[0]: the link eth1 is in VRF a and in VRF b"},
		"a Network of two VRFs": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`), vrf("b", `{"table": 200, "interfaces": ["eth2"]}`)}, "Network": {net("20-both", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network/20-both: spec.match: Network 20-both matches links of VRF a and VRF b; the links of one Network are in one VRF"},
		"a Network of a VRF and none": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("20-both", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network/20-both: spec.match: Network 20-both matches links of no VRF and VRF a; the links of one Network are in one VRF"},
		"a Network's VRF= of another VRF": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("20-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "b"}}`)}},
			"Network/20-eth1: spec.network.vrf: Network 20-eth1 gives its links VRF=b, but VRF a holds them"},
		"a Network of a VRF member and other links, before the VRF= naming it": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("10-a", `{"match": {"name": "eth1 eth2"}}`), net("20-b", `{"match": {"name": "eth1"}, "network": {"vrf": "tenant"}}`)}},
			"Network/10-a: spec.match: Network 10-a is the first to match eth1, so networkd never applies Network 20-b's VRF=tenant to it; give 10-a VRF=tenant, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network of a VRF member and other links, after the VRF= naming it": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("20-b", `{"match": {"name": "eth1"}, "network": {"vrf": "tenant"}}`), net("10-a", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network/10-a: spec.match: Network 10-a is the first to match eth1, so networkd never applies Network 20-b's VRF=tenant to it; give 10-a VRF=tenant, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network before the one whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5"}}`), net("20-b", `{"match": {"name": "eth5"}, "network": {"vrf": "mgmt"}}`)}},
			"Network/10-a: spec.match: Network 10-a is the first to match eth5, so networkd never applies Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network before the one whose VRF= globs its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5"}}`), net("20-b", `{"match": {"name": "eth*"}, "network": {"vrf": "mgmt"}}`)}},
			"Network/10-a: spec.match: Network 10-a is the first to match links of eth*, so networkd never applies Network 20-b's VRF=mgmt to them; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network of other keys before the one whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:01"}}`), net("20-b", `{"match": {"name": "eth5"}, "network": {"vrf": "mgmt"}}`)}},
			"Network/10-a: spec.match: Network 10-a may be the first to match eth5, so networkd may not apply Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network of other keys before one of other keys whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:01"}}`), net("20-b", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:02"}, "network": {"vrf": "mgmt"}}`)}},
			"Network/10-a: spec.match: Network 10-a may be the first to match eth5, so networkd may not apply Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt or drop 20-b's VRF="},
		"a Network before one of negations whose VRF= holds its link": {map[string][]any{"Network": {net("05-a", `{"match": {"name": "eth5"}}`), net("10-b", `{"match": {"name": "!eth0"}, "network": {"vrf": "mgmt"}}`)}},
			"Network/05-a: spec.match: Network 05-a is the first to match links Network 10-b's Name= does not negate, so networkd never applies Network 10-b's VRF=mgmt to them; give 05-a VRF=mgmt, rename it to sort after 10-b, or drop 10-b's VRF="},
		"a Network after one of negations whose VRF= holds the rest": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "!eth0"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth0"}}`)}},
			"Network/20-b: spec.match: Network 20-b matches links Network 10-a's Name= negates when Network 10-a does not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of a negation whose VRF= globs its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth* !eth1"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth1"}}`)}},
			"Network/20-b: spec.match: Network 20-b matches links of eth* when Network 10-a does not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of negations and other keys whose VRF= holds its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "!eth0", "macAddress": "02:00:00:00:00:01"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth5"}}`)}},
			"Network/20-b: spec.match: Network 20-b matches links Network 10-a's Name= does not negate when the other [Match] keys of Network 10-a do not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of other keys alone whose VRF= holds its link": {map[string][]any{"Network": {net("10-a", `{"match": {"macAddress": "02:00:00:00:00:01"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth5"}}`)}},
			"Network/20-b: spec.match: Network 20-b matches links Network 10-a may match when the other [Match] keys of Network 10-a do not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of other keys whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:01"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth5"}}`)}},
			"Network/20-b: spec.match: Network 20-b matches eth5 when the other [Match] keys of Network 10-a do not, and networkd then does not apply 10-a's VRF=mgmt to it; give 20-b VRF=mgmt"},
		"two Networks' VRF= of one link": {map[string][]any{"Network": {net("20-b", `{"match": {"name": "eth*"}, "network": {"vrf": "y"}}`), net("10-a", `{"match": {"name": "eth1"}, "network": {"vrf": "x"}}`)}},
			"Network/20-b: spec.match.name: Network 10-a gives VRF=x and Network 20-b VRF=y to links both may match"},
		"a Network glob of a VRF member and other links": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["swp1"]}`)}, "Network": {net("10-swp", `{"match": {"name": "swp*"}}`)}},
			"Network/10-swp: spec.match.name: Network 10-swp's Name=swp* may match links of VRF tenant and links outside it; name its links"},
		"a Network glob wider than a VRF's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["swp*"]}`)}, "Network": {link("swp1"), net("30-sw", `{"match": {"name": "sw*"}}`)}},
			"Network/30-sw: spec.match.name: Network 30-sw's Name=sw* may match links of VRF tenant and links outside it; name its links"},
		"a Network of every link before a member's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth2"]}`)}, "Network": {net("05-all", `{"match": {"name": "*"}}`), link("eth2")}},
			"Network/05-all: spec.match.name: Network 05-all's Name=* may match links of VRF tenant and links outside it; name its links"},
		"a Network of negations before a member's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth2"]}`)}, "Network": {net("05-rest", `{"match": {"name": "!eth0"}}`), link("eth2")}},
			"Network/05-rest: spec.match: Network 05-rest matches every link its Name= does not negate, links of VRF tenant and links outside it; name its links"},
		"a Network of a reset Name= and other keys before a member's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth2"]}`)}, "Network": {net("05-rest", `{"match": {"name": ["eth2", ""], "macAddress": "02:00:00:00:00:01"}}`), link("eth2")}},
			"Network/05-rest: spec.match: Network 05-rest matches by its other [Match] keys alone, as its Name= holds no word networkd takes, so it may match links of VRF tenant and links outside it; name its links"},
		"a member only a Network of other keys matches": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("10-eth1", `{"match": {"name": "eth1", "macAddress": "02:00:00:00:00:01"}}`)}},
			"VRF/tenant: spec.interfaces[0]: VRF tenant names the interface eth1, which no Network document's [Match] Name= matches without other [Match] keys; VRF= reaches a link through its Network document, so give it one"},
		"a Network before the VRF device's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("00-all", `{"match": {"name": "t*"}}`)}},
			"Network/00-all: metadata.name: Network 00-all matches the device of VRF tenant before 00-vrf-tenant; rename it to sort after 00-vrf-tenant"},
		"a Network of other keys before the VRF device's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("00-all", `{"match": {"name": "t*", "macAddress": "02:00:00:00:00:01"}}`)}},
			"Network/00-all: metadata.name: Network 00-all may match the device of VRF tenant before 00-vrf-tenant, should its other [Match] keys match it; rename it to sort after 00-vrf-tenant"},
		"globs of two VRFs": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["swp*"]}`), vrf("b", `{"table": 200, "interfaces": ["swp1*"]}`)}, "Network": {link("eth0")}},
			"VRF/b: spec.interfaces[0]: VRF a's swp* and VRF b's swp1* may hold one link"},
		"a VRF glob of a VRF device": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["t*"]}`), vrf("tenant", `{"table": 200}`)}, "Network": {link("eth0")}},
			"VRF/a: spec.interfaces[0]: VRF a's t* covers the device of VRF tenant"},
		"a Name= glob the router cannot match": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {net("20-x", `{"match": {"name": "eth[0"}}`)}},
			`Network/20-x: spec.match.name: Name= holds "eth[0", which is no glob the router can match as networkd does`},
		"a VRF= that adds a word": {map[string][]any{"Network": {net("20-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "b c"}}`)}},
			"Network/20-eth1: spec.network.vrf: VRF= names one VRF by its name, not \"b c\""},
		"a VRF of a link's name": {map[string][]any{"VRF": {vrf("eth1", `{"table": 100}`)}, "Network": {link("eth1")}},
			"VRF/eth1: metadata.name: a Network document names the link eth1, the VRF's device"},
		// Guards of what networkd and nftables could not load, or would read as syntax.
		"a VRF named default": {map[string][]any{"VRF": {vrf("default", `{"table": 100}`)}, "Network": {link("eth0")}},
			"internal error: VRF default: a VRF is named as its device, in 15 characters at most, and not default"},
		"a VRF name of 16 characters": {map[string][]any{"VRF": {vrf("tenant-12345678x", `{"table": 100}`)}, "Network": {link("eth0")}},
			"internal error: VRF tenant-12345678x: a VRF is named as its device, in 15 characters at most, and not default"},
		"table 0": {map[string][]any{"VRF": {vrf("a", `{"table": 0}`)}, "Network": {link("eth0")}},
			"internal error: VRF a: table 0 is no table of a VRF"},
		"a VRF name that adds a section": {map[string][]any{"VRF": {vrf("a\n[Network]", `{"table": 100}`)}},
			`internal error: a VRF document: "a\n[Network]" is no document name`},
		"an interface that adds a key": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1\nVRF=b"]}`)}, "Network": {link("eth1")}},
			`internal error: VRF a: "eth1\nVRF=b" is no interface name or glob`},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, c.docs, c.want) })
	}
}
