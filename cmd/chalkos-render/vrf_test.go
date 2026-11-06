package main

import (
	"reflect"
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

// The VRFs' units refuse a Network of a VRF unit's name and a NetDev of a VRF's device in any
// [NetDev] section: the names of the units the renderer adds, and NetDev documents, which the
// validator router does not read. The model refuses the rest, as derive's tests show.
func TestVRFRefusals(t *testing.T) {
	vrf := func(name, spec string) any { return routerDoc(t, "VRF", name, spec) }
	net := func(name, spec string) any { return document(t, "Network", name, spec) }
	link := func(name string) any { return net("20-"+name, `{"match": {"name": "`+name+`"}}`) }
	args := []string{"router-networkd", networkFixture}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"a NetDev of a VRF's device in a list of names": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {link("eth0")}, "NetDev": {document(t, "NetDev", "20-a", `{"netDev": {"kind": "dummy", "name": ["x", "a"]}}`)}},
			"NetDev 20-a creates the device a, which VRF a creates; rename the VRF or the NetDev"},
		"a NetDev of a VRF's device": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {link("eth0")}, "NetDev": {document(t, "NetDev", "20-a", `{"netDev": {"kind": "dummy", "name": "a"}}`)}},
			"NetDev 20-a creates the device a, which VRF a creates; rename the VRF or the NetDev"},
		"a NetDev of a VRF's device in a second section": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {link("eth0")}, "NetDev": {document(t, "NetDev", "20-a", `{"netDev": [{"kind": "dummy", "name": "x"}, {"kind": "dummy", "name": "a"}]}`)}},
			"NetDev 20-a creates the device a, which VRF a creates; rename the VRF or the NetDev"},
		"a document of a VRF unit's name": {map[string][]any{"VRF": {vrf("a", `{"table": 100}`)}, "Network": {net("00-vrf-a", `{"match": {"name": "eth0"}}`)}},
			"Network 00-vrf-a takes the name of VRF a's units"},
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
	// The NAT64's rules refuse a name whose link is longer than an interface's name.
	refused(t, args, map[string][]any{"NAT64": {nat64("abcdefghijkl")}},
		"internal error: NAT64 abcdefghijkl: its link n64-abcdefghijkl is no interface name of 15 characters; chalkd checks the config against the schemas, the rules and the validators before it renders")
	// A .netdev of the unit's name is another file than the unit's .network.
	if _, err := unitFiles(t, args, map[string][]any{"NAT64": {nat64("edge")}, "NetDev": {document(t, "NetDev", "00-n64-edge", `{"netDev": {"kind": "dummy", "name": "d0"}}`)}}); err != nil {
		t.Errorf("a NetDev of the unit's name: %v", err)
	}
}
