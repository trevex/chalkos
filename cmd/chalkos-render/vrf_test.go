package main

import (
	"reflect"
	"testing"
)

// router-networkd renders a VRF's device, of its table, and the .network that brings it up, and
// gives VRF= to the Networks of the links the VRF holds by name or by glob, in their first
// [Network] section, which a Network without one gains. The other units stay as networkd renders
// them.
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
		"00-vrf-tenant.netdev":  "[NetDev]\nKind=vrf\nName=tenant\n\n[VRF]\nTable=100\n\n",
		"00-vrf-tenant.network": "[Match]\nName=tenant\n\n",
		"10-eth0.network":       "[Match]\nName=eth0\n\n[Network]\nDHCP=yes\n\n",
		"20-eth2.network":       "[Match]\nName=eth2\n\n[Network]\nAddress=192.0.2.1/24\nVRF=tenant\n\n[Network]\nDNS=192.0.2.53\n\n",
		"30-swp.network":        "[Match]\nName=swp1 swp2\n\n[Network]\nVRF=tenant\n\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("units:\n%q\nwant:\n%q", got, want)
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
			"VRF tenant names the interface eth9, which no Network document's [Match] Name= names"},
		"a link a glob puts in a second VRF": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth*"]}`), vrf("b", `{"table": 200, "interfaces": ["eth1"]}`)}, "Network": {link("eth1")}},
			"the link eth1 is in VRF a and in VRF b"},
		"a Network of two VRFs": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`), vrf("b", `{"table": 200, "interfaces": ["eth2"]}`)}, "Network": {net("20-both", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network 20-both matches links of VRF a and VRF b"},
		"a Network of a VRF and none": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("20-both", `{"match": {"name": "eth1 eth2"}}`)}},
			"Network 20-both matches links of no VRF and VRF a"},
		"a Network's VRF= of another VRF": {map[string][]any{"VRF": {vrf("a", `{"table": 100, "interfaces": ["eth1"]}`)}, "Network": {net("20-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "b"}}`)}},
			"Network 20-eth1 gives its links VRF=b, but VRF a holds them"},
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
