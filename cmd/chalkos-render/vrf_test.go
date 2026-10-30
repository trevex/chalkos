package main

import (
	"reflect"
	"strings"
	"testing"
)

// router-networkd renders a VRF's device, of its table, and the .network that brings it up and
// ends the table in unreachable default routes of both families, and gives VRF= to the Networks
// of the links the VRF holds by name or by glob, in their first [Network] section, which a
// Network without one gains. The other units stay as networkd renders them.
func TestRouterNetworkd(t *testing.T) {
	got, err := unitFiles(t, []string{"router-networkd", networkFixture}, map[string][]any{
		"VRF": {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2", "swp*"]}`)},
		"Network": {
			document(t, "Network", "10-eth0", `{"match": {"name": "eth0"}, "network": {"dhcp": "yes"}}`),
			document(t, "Network", "20-eth2", `{"match": {"name": "eth2"}, "network": [{"address": ["192.0.2.1/24"]}, {"dns": "192.0.2.53"}]}`),
			document(t, "Network", "30-swp", `{"match": {"name": "swp1 swp2"}}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"00-vrf-tenant.netdev": "[NetDev]\nKind=vrf\nName=tenant\n\n[VRF]\nTable=100\n\n",
		"00-vrf-tenant.network": "[Match]\nName=tenant\n\n[Link]\nRequiredForOnline=no\n\n" +
			"[Route]\nDestination=0.0.0.0/0\nMetric=4278198272\nTable=100\nType=unreachable\n\n" +
			"[Route]\nDestination=::/0\nMetric=4278198272\nTable=100\nType=unreachable\n\n",
		"10-eth0.network": "[Match]\nName=eth0\n\n[Network]\nDHCP=yes\n\n",
		"20-eth2.network": "[Match]\nName=eth2\n\n[Network]\nAddress=192.0.2.1/24\nVRF=tenant\n\n[Network]\nDNS=192.0.2.53\n\n",
		"30-swp.network":  "[Match]\nName=swp1 swp2\n\n[Network]\nVRF=tenant\n\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("units:\n%q\nwant:\n%q", got, want)
	}
}

// A Network whose Name= globs a VRF's members, or whose own VRF= names the VRF, gets VRF= once;
// a Network that a member's earlier Network shadows gets none, and an empty VRF= resets the
// values before it.
func TestRouterNetworkdMembers(t *testing.T) {
	got, err := unitFiles(t, []string{"router-networkd", networkFixture}, map[string][]any{
		"VRF": {
			routerDoc(t, "VRF", "blue", `{"table": 200}`),
			routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["swp*", "eth3"]}`),
		},
		"Network": {
			document(t, "Network", "10-swp", `{"match": {"name": "swp[0-9]"}}`),
			document(t, "Network", "20-swp", `{"match": {"name": "swp*"}}`),
			document(t, "Network", "30-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "blue"}}`),
			document(t, "Network", "40-eth3", `{"match": {"name": "eth3"}, "network": {"vrf": ["blue", ""]}}`),
			document(t, "Network", "90-eth3", `{"match": {"name": "eth3"}}`),
			document(t, "Network", "99-all", `{"match": {"name": "*"}, "network": {"dhcp": "yes"}}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"10-swp.network":  "[Match]\nName=swp[0-9]\n\n[Network]\nVRF=tenant\n\n",
		"20-swp.network":  "[Match]\nName=swp*\n\n[Network]\nVRF=tenant\n\n",
		"30-eth1.network": "[Match]\nName=eth1\n\n[Network]\nVRF=blue\n\n",
		"40-eth3.network": "[Match]\nName=eth3\n\n[Network]\nVRF=blue\nVRF=\nVRF=tenant\n\n",
		"90-eth3.network": "[Match]\nName=eth3\n\n",
		"99-all.network":  "[Match]\nName=*\n\n[Network]\nDHCP=yes\n\n",
	} {
		if got[name] != want {
			t.Errorf("%s:\n%q\nwant:\n%q", name, got[name], want)
		}
	}
}

// The links of the Network documents are in the VRF whose document or whose Network's own VRF=
// holds them; the Networks are in the VRF of the links they are first to match.
func TestVRFLinks(t *testing.T) {
	byKind := map[string]any{"Network": []any{
		document(t, "Network", "10-swp", `{"match": {"name": "swp1 swp2"}}`),
		document(t, "Network", "20-swp", `{"match": {"name": "swp*"}}`),
		document(t, "Network", "30-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "blue"}}`),
		document(t, "Network", "40-eth0", `{"match": {"name": "eth0"}}`),
	}}
	got, err := vrfLinks([]named[vrfSpec]{{"tenant", vrfSpec{Table: 100, Interfaces: []string{"swp*"}}}}, networkLinks(byKind), byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"swp1": "tenant", "swp2": "tenant", "eth1": "blue", "eth0": defaultVRF} {
		if vrf, err := got.linkVRF(link); err != nil || vrf != want {
			t.Errorf("linkVRF(%s) = %q, %v, want %q", link, vrf, err, want)
		}
	}
	if want := map[string]string{"10-swp": "tenant", "20-swp": "tenant", "30-eth1": "blue"}; !reflect.DeepEqual(got.Networks, want) {
		t.Errorf("networks = %v, want %v", got.Networks, want)
	}
}

// Name= matches as systemd's does: globs per word, split at spaces, tabs and line breaks, a
// first word with ! negating every word, a later word with ! negating itself, a value of only
// whitespace resetting the values before it, and words systemd takes for no interface name, an
// interface index among them, left out. A backslash and a [ in a bracket expression, which
// fnmatch and path.Match read differently, are refused.
func TestNameMatch(t *testing.T) {
	for _, c := range []struct {
		names   any
		matches []string
		misses  []string
	}{
		{"eth0", []string{"eth0"}, []string{"eth1", "eth00"}},
		{"eth* swp[12]", []string{"eth0", "swp2"}, []string{"swp3", "lo"}},
		{"swp[!1]", []string{"swp2", "swp!"}, []string{"swp1", "swp"}},
		{"!eth0 eth1", []string{"lo", "eth2"}, []string{"eth0", "eth1"}},
		{"eth* !eth1", []string{"eth0"}, []string{"eth1", "lo"}},
		{[]any{"eth0", "eth1"}, []string{"eth0", "eth1"}, []string{"eth2"}},
		{[]any{"eth0", "", "eth1"}, []string{"eth1"}, []string{"eth0"}},
		{[]any{"eth0", ""}, []string{"eth0", "lo"}, nil},
		{"eth0:1 eth1", []string{"eth1"}, []string{"eth0:1", "lo"}},
		{nil, []string{"eth0"}, nil},
		{[]any{"eth0", " \t "}, []string{"eth0", "lo"}, nil},
		{"eth1 eth0\veth2", []string{"eth1"}, []string{"eth0", "eth2", "eth0\veth2"}},
		{"0x1f 0b1 0o7 +5 017 08 0 eth1", []string{"eth1"}, []string{"0x1f", "0b1", "0o7", "+5", "017", "08", "0"}},
		{"-5 0x 0x100000000 eth1", []string{"-5", "0x", "0x100000000", "eth1"}, []string{"lo"}},
		{"0x7fffffff 0x80000000 eth1", []string{"0x80000000", "eth1"}, []string{"0x7fffffff"}},
		{"0b0b1 +0b1 0b+1 eth1", []string{"eth1"}, []string{"0b0b1", "+0b1", "0b+1"}},
	} {
		m, err := parseNameMatch("n", map[string]any{"match": map[string]any{"name": c.names}})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range c.matches {
			if !m.matches(l) {
				t.Errorf("Name=%v misses %s", c.names, l)
			}
		}
		for _, l := range c.misses {
			if m.matches(l) {
				t.Errorf("Name=%v matches %s", c.names, l)
			}
		}
	}
	for _, name := range []string{"eth[0", `eth\0`, `eth1 eth\`, "eth[[=a=]]", "eth[[.a.]]", "eth[a[]"} {
		if _, err := parseNameMatch("n", map[string]any{"match": map[string]any{"name": name}}); err == nil {
			t.Errorf("parseNameMatch takes Name=%s", name)
		}
	}
}

// Without VRF documents, router-networkd renders what networkd does, a Network's own VRF=
// included.
func TestRouterNetworkdWithoutVRFs(t *testing.T) {
	docs := map[string][]any{"Network": {
		document(t, "Network", "10-eth0", `{"match": {"name": "eth0"}, "network": {"dhcp": "yes"}}`),
		document(t, "Network", "20-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "mgmt"}}`),
	}}
	plain, err := networkd(t, networkFixture, docs)
	if err != nil {
		t.Fatal(err)
	}
	router, err := unitFiles(t, []string{"router-networkd", networkFixture}, docs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, router) {
		t.Errorf("router-networkd:\n%q\nnetworkd:\n%q", router, plain)
	}
}

// Without VRF documents and VRF=, router-networkd and frr take a Name= the VRF checks cannot
// read, such as one of a backslash or of an unclosed bracket, as router images did before VRFs.
func TestRouterWithoutVRFsTakesAnyName(t *testing.T) {
	docs := map[string][]any{"Network": {
		document(t, "Network", "10-eth1", `{"match": {"name": "eth\\x31"}}`),
		document(t, "Network", "20-eth", `{"match": {"name": "eth["}}`),
	}}
	plain, err := networkd(t, networkFixture, docs)
	if err != nil {
		t.Fatal(err)
	}
	router, err := unitFiles(t, []string{"router-networkd", networkFixture}, docs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, router) {
		t.Errorf("router-networkd:\n%q\nnetworkd:\n%q", router, plain)
	}
	if _, err := unitFiles(t, []string{"frr"}, docs); err != nil {
		t.Errorf("frr: %v", err)
	}
}

// The VRFs' units refuse what networkd would render differently from the documents, and values
// that would add to a unit.
func TestVRFRefusals(t *testing.T) {
	vrf := func(name, spec string) any { return routerDoc(t, "VRF", name, spec) }
	net := func(name, spec string) any { return document(t, "Network", name, spec) }
	link := func(name string) any { return net("20-"+name, `{"match": {"name": "`+name+`"}}`) }
	args := []string{"router-networkd", networkFixture}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a member no Network names": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth9"]}`)}, "Network": {link("eth1")}},
			"VRF tenant names the interface eth9, which no Network document's [Match] Name= matches"},
		"a link a glob puts in a second VRF": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth*"]}`), vrf("b", `{"table": 200, "interfaces": ["eth1"]}`)}, "Network": {link("eth1")}},
			"the link eth1 is in VRF a and in VRF b"},
		"a Network of two VRFs": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`), vrf("b", `{"table": 200, "interfaces": ["eth2"]}`)}, "Network": {net("20-both", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network 20-both matches links of VRF a and VRF b"},
		"a Network of a VRF and none": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("20-both", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network 20-both matches links of no VRF and VRF a"},
		"a Network's VRF= of another VRF": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("20-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "b"}}`)}},
			"Network 20-eth1 gives its links VRF=b, but VRF a holds them"},
		"a Network of a VRF member and other links, before the VRF= naming it": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("10-a", `{"match": {"name": "eth1 eth2"}}`), net("20-b", `{"match": {"name": "eth1"}, "network": {"vrf": "tenant"}}`)}},
			"Network 10-a is the first to match eth1, so networkd never applies Network 20-b's VRF=tenant to it"},
		"a Network of a VRF member and other links, after the VRF= naming it": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("20-b", `{"match": {"name": "eth1"}, "network": {"vrf": "tenant"}}`), net("10-a", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network 10-a is the first to match eth1, so networkd never applies Network 20-b's VRF=tenant to it"},
		"a Network before the one whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5"}}`), net("20-b", `{"match": {"name": "eth5"}, "network": {"vrf": "mgmt"}}`)}},
			"Network 10-a is the first to match eth5, so networkd never applies Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network before the one whose VRF= globs its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5"}}`), net("20-b", `{"match": {"name": "eth*"}, "network": {"vrf": "mgmt"}}`)}},
			"Network 10-a is the first to match links of eth*, so networkd never applies Network 20-b's VRF=mgmt to them; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network of other keys before the one whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:01"}}`), net("20-b", `{"match": {"name": "eth5"}, "network": {"vrf": "mgmt"}}`)}},
			"Network 10-a may be the first to match eth5, so networkd may not apply Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF="},
		"a Network of other keys before one of other keys whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:01"}}`), net("20-b", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:02"}, "network": {"vrf": "mgmt"}}`)}},
			"Network 10-a may be the first to match eth5, so networkd may not apply Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt or drop 20-b's VRF="},
		"a Network before one of negations whose VRF= holds its link": {map[string][]any{"Network": {net("05-a", `{"match": {"name": "eth5"}}`), net("10-b", `{"match": {"name": "!eth0"}, "network": {"vrf": "mgmt"}}`)}},
			"Network 05-a is the first to match links Network 10-b's Name= does not negate, so networkd never applies Network 10-b's VRF=mgmt to them; give 05-a VRF=mgmt, rename it to sort after 10-b, or drop 10-b's VRF="},
		"a Network after one of negations whose VRF= holds the rest": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "!eth0"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth0"}}`)}},
			"Network 20-b matches links Network 10-a's Name= negates when Network 10-a does not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of a negation whose VRF= globs its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth* !eth1"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth1"}}`)}},
			"Network 20-b matches links of eth* when Network 10-a does not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of negations and other keys whose VRF= holds its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "!eth0", "macAddress": "02:00:00:00:00:01"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth5"}}`)}},
			"Network 20-b matches links Network 10-a's Name= does not negate when the other [Match] keys of Network 10-a do not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of other keys alone whose VRF= holds its link": {map[string][]any{"Network": {net("10-a", `{"match": {"macAddress": "02:00:00:00:00:01"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth5"}}`)}},
			"Network 20-b matches links Network 10-a may match when the other [Match] keys of Network 10-a do not, and networkd then does not apply 10-a's VRF=mgmt to them; give 20-b VRF=mgmt"},
		"a Network after one of other keys whose VRF= names its link": {map[string][]any{"Network": {net("10-a", `{"match": {"name": "eth5", "macAddress": "02:00:00:00:00:01"}, "network": {"vrf": "mgmt"}}`), net("20-b", `{"match": {"name": "eth5"}}`)}},
			"Network 20-b matches eth5 when the other [Match] keys of Network 10-a do not, and networkd then does not apply 10-a's VRF=mgmt to it; give 20-b VRF=mgmt"},
		"two Networks' VRF= of one link": {map[string][]any{"Network": {net("20-b", `{"match": {"name": "eth*"}, "network": {"vrf": "y"}}`), net("10-a", `{"match": {"name": "eth1"}, "network": {"vrf": "x"}}`)}},
			"Network 10-a gives VRF=x and Network 20-b VRF=y to links both may match"},
		"a Network glob of a VRF member and other links": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["swp1"]}`)}, "Network": {net("10-swp", `{"match": {"name": "swp*"}}`)}},
			"Network 10-swp's Name=swp* may match links of VRF tenant and links outside it; name its links"},
		"a Network glob wider than a VRF's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["swp*"]}`)}, "Network": {link("swp1"), net("30-sw", `{"match": {"name": "sw*"}}`)}},
			"Network 30-sw's Name=sw* may match links of VRF tenant and links outside it; name its links"},
		"a Network of every link before a member's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth2"]}`)}, "Network": {net("05-all", `{"match": {"name": "*"}}`), link("eth2")}},
			"Network 05-all's Name=* may match links of VRF tenant and links outside it; name its links"},
		"a Network of negations before a member's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth2"]}`)}, "Network": {net("05-rest", `{"match": {"name": "!eth0"}}`), link("eth2")}},
			"Network 05-rest matches every link its Name= does not negate, links of VRF tenant and links outside it; name its links"},
		"a Network of a reset Name= and other keys before a member's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth2"]}`)}, "Network": {net("05-rest", `{"match": {"name": ["eth2", ""], "macAddress": "02:00:00:00:00:01"}}`), link("eth2")}},
			"Network 05-rest matches by its other [Match] keys alone, as its Name= holds no word networkd takes, so it may match links of VRF tenant and links outside it; name its links"},
		"a member only a Network of other keys matches": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("10-eth1", `{"match": {"name": "eth1", "macAddress": "02:00:00:00:00:01"}}`)}},
			"VRF tenant names the interface eth1, which no Network document's [Match] Name= matches without other [Match] keys"},
		"a Network before the VRF device's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("00-all", `{"match": {"name": "t*"}}`)}},
			"Network 00-all matches the device of VRF tenant before 00-vrf-tenant; rename it to sort after 00-vrf-tenant"},
		"a Network of other keys before the VRF device's": {map[string][]any{"VRF": {vrf("tenant", `{"table": 100}`)}, "Network": {net("00-all", `{"match": {"name": "t*", "macAddress": "02:00:00:00:00:01"}}`)}},
			"Network 00-all may match the device of VRF tenant before 00-vrf-tenant, should its other [Match] keys match it; rename it to sort after 00-vrf-tenant"},
		"globs of two VRFs": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["swp*"]}`), vrf("b", `{"table": 200, "interfaces": ["swp1*"]}`)}, "Network": {link("eth0")}},
			"VRF a's swp* and VRF b's swp1* may hold one link"},
		"a VRF glob of a VRF device": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["t*"]}`), vrf("tenant", `{"table": 200}`)}, "Network": {link("eth0")}},
			"VRF a's t* covers the device of VRF tenant"},
		"a NetDev of a VRF's device in a list of names": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {link("eth0")}, "NetDev": {document(t, "NetDev", "20-a", `{"netDev": {"kind": "dummy", "name": ["x", "a"]}}`)}},
			"NetDev 20-a creates the device a, which VRF a creates"},
		"a Name= glob the renderer cannot read": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {net("20-x", `{"match": {"name": "eth[0"}}`)}},
			`Network 20-x: Name= holds "eth[0"`},
		"a VRF= that adds a word": {map[string][]any{"Network": {net("20-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "b c"}}`)}},
			"Network 20-eth1: VRF= names one VRF by its name"},
		"a NetDev of a VRF's device": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {link("eth0")}, "NetDev": {document(t, "NetDev", "20-a", `{"netDev": {"kind": "dummy", "name": "a"}}`)}},
			"NetDev 20-a creates the device a, which VRF a creates"},
		"a document of a VRF unit's name": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {net("00-vrf-a", `{"match": {"name": "eth0"}}`)}},
			"Network 00-vrf-a takes the name of VRF a's units"},
		"a VRF of a link's name": {map[string][]any{"VRF": {vrf("eth1", `{"table": 100}`)}, "Network": {link("eth1")}},
			"VRF eth1: a Network document names the link eth1, the VRF's device"},
		"the main table": {map[string][]any{"VRF": {vrf("a", `{"table": 254}`)}, "Network": {link("eth0")}},
			"VRF a: table 254 is no table of a VRF"},
		"two VRFs of one table": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`), vrf("b", `{"table": 100}`)}, "Network": {link("eth0")}},
			"VRF a and VRF b both use table 100"},
		"a VRF named default": {map[string][]any{"VRF": {vrf("default", `{"table": 100}`)}, "Network": {link("eth0")}},
			"VRF default: a VRF is named as its device"},
		"a VRF name that adds a section": {map[string][]any{"VRF": {vrf("a\n[Network]", `{"table": 100}`)}},
			"is no document name"},
		"an interface that adds a key": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1\nVRF=b"]}`)}, "Network": {link("eth1")}},
			`"eth1\nVRF=b" is no interface name or glob`},
	} {
		t.Run(name, func(t *testing.T) { refused(t, args, c.docs, c.want) })
	}
}

// A Network whose other [Match] keys narrow its Name= may match the links its Name= does, or
// not: it gets VRF= for a VRF's links as later Networks that match them do, and it hides them
// from none. A Network networkd ignores, of no [Match] it takes, gets none.
func TestRouterNetworkdNarrowedMatch(t *testing.T) {
	mac := `, "macAddress": "02:00:00:00:00:01"`
	got, err := unitFiles(t, []string{"router-networkd", networkFixture}, map[string][]any{
		"VRF": {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth1", "swp*"]}`)},
		"Network": {
			document(t, "Network", "05-none", `{"match": {"name": "eth1:0"}}`),
			document(t, "Network", "10-eth1", `{"match": {"name": "eth1"`+mac+`}}`),
			document(t, "Network", "10-swp1", `{"match": {"name": "swp1"`+mac+`}}`),
			document(t, "Network", "20-eth1", `{"match": {"name": "eth1"}}`),
			document(t, "Network", "20-swp1", `{"match": {"name": "swp1"}}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"05-none.network": "[Match]\nName=eth1:0\n\n",
		"10-eth1.network": "[Match]\nMACAddress=02:00:00:00:00:01\nName=eth1\n\n[Network]\nVRF=tenant\n\n",
		"10-swp1.network": "[Match]\nMACAddress=02:00:00:00:00:01\nName=swp1\n\n[Network]\nVRF=tenant\n\n",
		"20-eth1.network": "[Match]\nName=eth1\n\n[Network]\nVRF=tenant\n\n",
		"20-swp1.network": "[Match]\nName=swp1\n\n[Network]\nVRF=tenant\n\n",
	} {
		if got[name] != want {
			t.Errorf("%s:\n%q\nwant:\n%q", name, got[name], want)
		}
	}
}

// networkd puts a link in the VRF of the Network it picks, which a Network whose other [Match]
// keys may not match leaves open unless a Network that matches the link by Name= alone follows.
func TestLinkVRFNarrowed(t *testing.T) {
	mac := `, "macAddress": "02:00:00:00:00:01"`
	byKind := map[string]any{"Network": []any{
		document(t, "Network", "10-swp", `{"match": {"name": "swp1 swp2"`+mac+`}}`),
		document(t, "Network", "20-swp2", `{"match": {"name": "swp2"}}`),
		document(t, "Network", "30-eth", `{"match": {"name": "eth0"`+mac+`}}`),
	}}
	m, err := vrfLinks([]named[vrfSpec]{{"tenant", vrfSpec{Table: 100, Interfaces: []string{"swp*"}}}}, networkLinks(byKind), byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"swp2": "tenant", "eth0": defaultVRF, "lo": defaultVRF} {
		if got, err := m.linkVRF(link); err != nil || got != want {
			t.Errorf("linkVRF(%s) = %q, %v, want %q", link, got, err, want)
		}
	}
	want := "networkd puts swp1 in VRF tenant only if the other [Match] keys of Network 10-swp match it"
	if _, err := m.linkVRF("swp1"); err == nil || !strings.Contains(err.Error(), want) {
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

// router-networkd leaves each NAT64's link unmanaged with a .network that networkd picks for it
// before the node's own, also without VRFs, and refuses a Network that networkd would pick first,
// a Network of the unit's name and a NetDev that creates a device of the link's name.
func TestNAT64Units(t *testing.T) {
	nat64 := func(name string) any {
		return routerDoc(t, "NAT64", name, `{"prefix": "64:ff9b::/96", "pool4": []}`)
	}
	got, err := unitFiles(t, []string{"router-networkd", networkFixture}, map[string][]any{
		"NAT64":   {nat64("edge")},
		"Network": {document(t, "Network", "10-all", `{"match": {"name": "*"}, "network": {"dhcp": "yes"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"00-n64-edge.network": "[Match]\nName=n64-edge\n\n[Link]\nUnmanaged=yes\n\n",
		"10-all.network":      "[Match]\nName=*\n\n[Network]\nDHCP=yes\n\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("units:\n%q\nwant:\n%q", got, want)
	}
	args := []string{"router-networkd", networkFixture}
	refused(t, args, map[string][]any{"NAT64": {nat64("edge")}, "Network": {document(t, "Network", "00-a", `{"match": {"name": "n64-*"}}`)}},
		"Network 00-a matches the link n64-edge of NAT64 edge before 00-n64-edge; rename it to sort after 00-n64-edge")
	refused(t, args, map[string][]any{"NAT64": {nat64("edge")}, "Network": {document(t, "Network", "00-a", `{"match": {"name": "n64-*", "macAddress": "02:00:00:00:00:01"}}`)}},
		"Network 00-a may match the link n64-edge of NAT64 edge before 00-n64-edge, should its other [Match] keys match it")
	refused(t, args, map[string][]any{"NAT64": {nat64("edge")}, "Network": {document(t, "Network", "00-n64-edge", `{"match": {"name": "eth0"}}`)}},
		"Network 00-n64-edge takes the name of NAT64 edge's unit; rename the document")
	refused(t, args, map[string][]any{"NAT64": {nat64("edge")}, "NetDev": {document(t, "NetDev", "10-dummy", `{"netDev": {"kind": "dummy", "name": "n64-edge"}}`)}},
		"NetDev 10-dummy creates the device n64-edge, the link of NAT64 edge; give the device another name")
	// A .netdev of the unit's name is another file than the unit's .network.
	if _, err := unitFiles(t, args, map[string][]any{"NAT64": {nat64("edge")}, "NetDev": {document(t, "NetDev", "00-n64-edge", `{"netDev": {"kind": "dummy", "name": "d0"}}`)}}); err != nil {
		t.Errorf("a NetDev of the unit's name: %v", err)
	}
}
