package derive

import (
	"encoding/json"
	"reflect"
	"testing"
)

// peerDoc is a BGPPeer document of the external AS that exchanges IPv6 routes, with more of its
// spec in JSON.
func peerDoc(t *testing.T, name, more string) any {
	t.Helper()
	return routerDoc(t, "BGPPeer", name, `{"remoteAS": "external", "families": {"ipv6": {}}, `+more+`}`)
}

// The sessions of a peer are its address, its interface or the links of its VRF its globs match,
// named as bgpd names them; a glob skips the links of other VRFs.
func TestSessions(t *testing.T) {
	byKind := map[string]any{
		"VRF":     []any{routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)},
		"Network": []any{document(t, "Network", "10-eth", `{"match": {"name": "eth0 eth1"}}`), document(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)},
		"BGPPeer": []any{
			peerDoc(t, "a", `"address": "2001:DB8::1"`),
			peerDoc(t, "b", `"interface": "eth0"`),
			peerDoc(t, "c", `"interfaces": ["eth1*"]`),
			peerDoc(t, "d", `"vrf": "tenant", "interfaces": ["eth*"]`),
		},
	}
	data, err := json.Marshal(byKind)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Build(decode(t, data).(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Session{
		"a": {{Neighbour: "2001:db8::1"}},
		"b": {{Neighbour: "eth0", OnLink: true}},
		"c": {{Neighbour: "eth1", OnLink: true}},
		"d": {{Neighbour: "eth2", OnLink: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sessions() = %v, want %v", got, want)
	}
}

// The validator refuses a peer's sessions where the links networkd puts in each VRF decide: an
// interface of a VRF's device or of another VRF than the peer's, whether the VRF names it or a
// glob of it, a glob of other VRFs' links alone, a link a glob expands to that is named like an
// address, and a session two peers name.
// It guards, with an internal error, values frr.conf and the firewall would read as syntax.
func TestSessionRefusals(t *testing.T) {
	tenant := routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)
	eth2 := document(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`)
	withTenant := func(peers ...any) map[string][]any {
		return map[string][]any{"VRF": {tenant}, "Network": {eth2}, "BGPPeer": peers}
	}
	for name, c := range map[string]struct {
		docs map[string][]any
		want string
	}{
		"two peers of one session": {map[string][]any{"BGPPeer": {peerDoc(t, "a", `"interface": "eth1"`), peerDoc(t, "b", `"interface": "eth1"`)}},
			"BGPPeer/b: spec.interface: BGPPeer a and BGPPeer b both name eth1 in the VRF default; give each session one peer"},
		"a link that reads as an address": {map[string][]any{"BGPPeer": {peerDoc(t, "a", `"interfaces": ["*"]`)}, "Network": {document(t, "Network", "n", `{"match": {"name": "192.0.2.10"}}`)}},
			`BGPPeer/a: spec.interfaces[0]: interface "192.0.2.10" is an address, which FRR cannot tell from a neighbour's`},
		"an interface of a VRF's device": {withTenant(peerDoc(t, "x", `"interface": "tenant"`)),
			"BGPPeer/x: spec.interface: interface tenant is the device of VRF tenant"},
		"interfaces of a VRF's device": {withTenant(peerDoc(t, "x", `"interfaces": ["ten*"]`)),
			"BGPPeer/x: spec.interfaces[0]: ten* matches tenant, the device of VRF tenant"},
		"an interface of the device of a Network's VRF=": {map[string][]any{"Network": {document(t, "Network", "20-eth3", `{"match": {"name": "eth3"}, "network": {"vrf": "lab"}}`)}, "BGPPeer": {peerDoc(t, "x", `"interface": "lab"`)}},
			"BGPPeer/x: spec.interface: interface lab is the device of VRF lab"},
		"an interface of another VRF": {withTenant(peerDoc(t, "x", `"interface": "eth2"`)),
			"BGPPeer/x: spec.interface: interface eth2 is in the VRF tenant, not in the peer's VRF default"},
		"an interface a VRF's glob holds": {map[string][]any{
			"VRF":     {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["swp*"]}`)},
			"Network": {document(t, "Network", "30-swp", `{"match": {"name": "swp*"}}`)},
			"BGPPeer": {peerDoc(t, "leaf", `"interface": "swp1"`)},
		}, "BGPPeer/leaf: spec.interface: interface swp1 is in the VRF tenant, not in the peer's VRF default"},
		"interfaces a VRF's glob holds alone": {map[string][]any{
			"VRF":     {routerDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["swp*"]}`)},
			"Network": {document(t, "Network", "30-swp", `{"match": {"name": "swp*"}}`), document(t, "Network", "20-swp1", `{"match": {"name": "swp1"}}`)},
			"BGPPeer": {peerDoc(t, "leaves", `"interfaces": ["sw*"]`)},
		}, "BGPPeer/leaves: spec.interfaces[0]: sw* matches no link of VRF default, only links of other VRFs"},
		// A VRF's interface reaches its link through a Network, so one that no Network names is
		// refused before a peer's interface of that name could be read as the default VRF's.
		"an interface of another VRF that no Network names": {map[string][]any{"VRF": {tenant}, "Network": {document(t, "Network", "10-eth0", `{"match": {"name": "eth0"}}`)}, "BGPPeer": {peerDoc(t, "x", `"interface": "eth2"`)}},
			"VRF/tenant: spec.interfaces[0]: VRF tenant names the interface eth2, which no Network document's [Match] Name= matches without other [Match] keys; VRF= reaches a link through its Network document, so give it one"},
		"interfaces of every link": {withTenant(peerDoc(t, "x", `"interfaces": ["*"]`)),
			"BGPPeer/x: spec.interfaces[0]: * matches tenant, the device of VRF tenant"},
		"interfaces of another VRF's links alone": {withTenant(peerDoc(t, "x", `"interfaces": ["eth1", "eth2*"]`)),
			"BGPPeer/x: spec.interfaces[1]: eth2* matches no link of VRF default, only links of other VRFs"},
		// Guards of what frr.conf and the firewall write unquoted.
		"an address that adds an argument": {map[string][]any{"BGPPeer": {peerDoc(t, "a", `"address": "192.0.2.9 remote-as 64999"`)}},
			`internal error: BGPPeer a: ParseAddr("192.0.2.9 remote-as 64999"): unexpected character`},
		"an address with a zone": {map[string][]any{"BGPPeer": {peerDoc(t, "a", `"address": "fe80::1%eth1"`)}},
			`internal error: BGPPeer a: spec.address: "fe80::1%eth1" has a zone`},
		"an interface that adds a command": {map[string][]any{"BGPPeer": {peerDoc(t, "a", `"interface": "eth1\n neighbor 192.0.2.9 remote-as 64999"`)}},
			`internal error: BGPPeer a: interface "eth1\n neighbor 192.0.2.9 remote-as 64999" is no interface name`},
		"interfaces that add a command": {map[string][]any{"BGPPeer": {peerDoc(t, "a", `"interfaces": ["eth1 remote-as 64999"]`)}},
			`internal error: BGPPeer a: interfaces: "eth1 remote-as 64999" is no interface name or glob`},
		"a peer of nothing": {map[string][]any{"BGPPeer": {routerDoc(t, "BGPPeer", "a", `{"remoteAS": "external", "families": {"ipv6": {}}}`)}},
			"internal error: BGPPeer a: interfaces names no interface"},
	} {
		t.Run(name, func(t *testing.T) { refusedOrGuarded(t, c.docs, c.want) })
	}
}
