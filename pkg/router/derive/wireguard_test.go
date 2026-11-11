package derive

import (
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// wireGuardDocs are a WireGuard wg0 in the VRF vrf, with the schema's defaults, a WireGuard wg1 of
// the default VRF, the VRF tenant of eth2 and the Network documents of eth0 and eth2, with the
// peers given and more documents of each kind.
func wireGuardDocs(t *testing.T, vrf string, peers []any, more map[string][]any) map[string][]any {
	t.Helper()
	docs := map[string][]any{
		"WireGuard": {
			derivetest.RouterDoc(t, "WireGuard", "wg0", `{"listenPort": 51820, "addresses": ["fd00:f0::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "wg0"}}, "vrf": "`+vrf+`"}`),
			derivetest.RouterDoc(t, "WireGuard", "wg1", `{"listenPort": 51821, "addresses": ["fd00:f1::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "wg1"}}, "vrf": "default"}`),
		},
		"WireGuardPeer": peers,
		"VRF":           {derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100, "interfaces": ["eth2"]}`)},
		"Network": {
			derivetest.Doc(t, "Network", "10-eth0", `{"match": {"name": "eth0"}}`),
			derivetest.Doc(t, "Network", "20-eth2", `{"match": {"name": "eth2"}}`),
		},
	}
	for kind, d := range more {
		docs[kind] = append(docs[kind], d...)
	}
	return docs
}

// wireGuardPeer is a WireGuardPeer of the interface with the public key and allowed IPs given.
func wireGuardPeer(t *testing.T, name, iface, key, allowed string) any {
	t.Helper()
	return derivetest.RouterDoc(t, "WireGuardPeer", name, `{"interface": "`+iface+`", "publicKey": "`+key+`", "allowedIPs": [`+allowed+`]}`)
}

// Two test keys.
const (
	keyA = "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU="
	keyB = "YCfBku3dYhut+x0pFQMw7WbWU5qVks5ndJDuewg4J2I="
)

// The validator accepts peers of distinct keys and allowed IPs on an interface, and one key and
// one range on two interfaces, and refuses a peer whose allowed IPs overlap an earlier peer's of
// its interface or that has an earlier peer's key there. It refuses a WireGuard whose link a VRF
// or a Network's VRF= puts in another VRF, and a Network networkd may pick for the link before
// the WireGuard's own unit, of that unit's name or whose Name= names the link. It refuses a peer of
// an endpoint networkd cannot read and one of a default route, and a VRF that names a WireGuard's
// interface.
func TestWireGuardChecks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		docs  map[string][]any
		wants []string
	}{
		{"peers of distinct keys and ranges", wireGuardDocs(t, "default", []any{
			wireGuardPeer(t, "a", "wg0", keyA, `"fd00:f0::2/128"`),
			wireGuardPeer(t, "b", "wg0", keyB, `"fd00:f0::3/128", "192.0.2.0/24"`),
		}, nil), nil},
		{"one key and range on two interfaces", wireGuardDocs(t, "default", []any{
			wireGuardPeer(t, "a", "wg0", keyA, `"fd00:f0::2/128"`),
			wireGuardPeer(t, "b", "wg1", keyA, `"fd00:f0::2/128"`),
		}, nil), nil},
		{"a WireGuard in a VRF", wireGuardDocs(t, "tenant", nil, nil), nil},
		{"overlapping allowed IPs", wireGuardDocs(t, "default", []any{
			wireGuardPeer(t, "a", "wg0", keyA, `"fd00:f0::/64"`),
			wireGuardPeer(t, "b", "wg0", keyB, `"192.0.2.0/24", "fd00:f0::3/128"`),
		}, nil), []string{"WireGuardPeer/b: spec.allowedIPs[1]: allowedIPs fd00:f0::3/128 overlaps fd00:f0::/64 of WireGuardPeer a on wg0; a WireGuard sends each address to one peer"}},
		{"one key twice", wireGuardDocs(t, "default", []any{
			wireGuardPeer(t, "a", "wg0", keyA, `"fd00:f0::2/128"`),
			wireGuardPeer(t, "b", "wg0", keyA, `"fd00:f0::3/128"`),
		}, nil), []string{"WireGuardPeer/b: spec.publicKey: WireGuardPeer b has the public key of WireGuardPeer a on wg0"}},
		{"a VRF's glob", wireGuardDocs(t, "default", nil, map[string][]any{
			"VRF": {derivetest.RouterDoc(t, "VRF", "lab", `{"table": 200, "interfaces": ["wg*"]}`)},
		}), []string{
			"WireGuard/wg0: spec.vrf: VRF lab holds wg0 by its wg*, but the WireGuard is in VRF default; give the WireGuard vrf lab, or keep its link out of VRF lab's interfaces",
			"WireGuard/wg1: spec.vrf: VRF lab holds wg1 by its wg*, but the WireGuard is in VRF default; give the WireGuard vrf lab, or keep its link out of VRF lab's interfaces",
		}},
		{"a Network's VRF=", wireGuardDocs(t, "tenant", nil, map[string][]any{
			"Network": {derivetest.Doc(t, "Network", "50-wg", `{"match": {"name": "wg*"}, "network": {"vrf": "tenant"}}`)},
		}), []string{"WireGuard/wg1: spec.vrf: Network 50-wg gives VRF=tenant to the links its Name=wg* matches, wg1 among them, but the WireGuard is in VRF default; a WireGuard's link takes the VRF of the WireGuard's vrf"}},
		{"a Network before the unit", wireGuardDocs(t, "default", nil, map[string][]any{
			"Network": {derivetest.Doc(t, "Network", "00-all", `{"match": {"name": "wg*"}}`)},
		}), []string{
			"Network/00-all: metadata.name: Network 00-all matches the link of WireGuard wg0 before 00-wg-wg0; rename it to sort after 00-wg-wg0",
			"Network/00-all: metadata.name: Network 00-all matches the link of WireGuard wg1 before 00-wg-wg1; rename it to sort after 00-wg-wg1",
		}},
		{"a Network of the unit's name", wireGuardDocs(t, "default", nil, map[string][]any{
			"Network": {derivetest.Doc(t, "Network", "00-wg-wg0", `{"match": {"name": "eth9"}}`)},
		}), []string{"Network/00-wg-wg0: metadata.name: Network 00-wg-wg0 takes the name of WireGuard wg0's unit; rename the document"}},
		{"a Network that names the link", wireGuardDocs(t, "default", nil, map[string][]any{
			"Network": {derivetest.Doc(t, "Network", "50-wg0", `{"match": {"name": "eth9 wg0"}}`)},
		}), []string{"Network/50-wg0: spec.match: Network 50-wg0 names wg0, the interface of WireGuard wg0, whose own unit configures it; drop wg0 from the Network"}},
		{"a VRF that names the interface", wireGuardDocs(t, "default", nil, map[string][]any{
			"VRF": {derivetest.RouterDoc(t, "VRF", "lab", `{"table": 200, "interfaces": ["wg0"]}`)},
		}), []string{"VRF/lab: spec.interfaces[0]: VRF lab names wg0, the interface of WireGuard wg0; set the WireGuard's vrf instead"}},
		{"a default route", wireGuardDocs(t, "default", []any{wireGuardPeer(t, "a", "wg0", keyA, `"fd00:f0::2/128", "::/0"`)}, nil),
			[]string{"WireGuardPeer/a: spec.allowedIPs[1]: allowedIPs ::/0 is a default route, which the router does not send through a peer; name the ranges behind the peer"}},
		{"an endpoint of no port", wireGuardDocs(t, "default", []any{derivetest.RouterDoc(t, "WireGuardPeer", "a", `{"interface": "wg0", "publicKey": "`+keyA+`", "allowedIPs": ["fd00:f0::2/128"], "endpoint": "[2001:db8::1]:0"}`)}, nil),
			[]string{`WireGuardPeer/a: spec.endpoint: endpoint "[2001:db8::1]:0" is none of [<IPv6>]:<port>, <IPv4>:<port> and <host name>:<port> with a port from 1 to 65535`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wants == nil {
				accepted(t, tc.docs)
				return
			}
			for _, want := range tc.wants {
				refused(t, tc.docs, want)
			}
		})
	}
}

// A WireGuard's link is a link of the model in the WireGuard's VRF: a peer on it runs there, and
// a zone of it holds that VRF.
func TestWireGuardLinkVRF(t *testing.T) {
	byKind, err := readDocuments(stdin(t, wireGuardDocs(t, "tenant", nil, nil)))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Build(byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"wg0": "tenant", "wg1": DefaultVRF, "eth2": "tenant", "eth0": DefaultVRF} {
		if got, err := d.Membership.LinkVRF(link); err != nil || got != want {
			t.Errorf("LinkVRF(%s) = %q, %v, want %q", link, got, err, want)
		}
	}
	if links, err := d.Membership.Links("tenant"); err != nil || len(links) != 2 || links[0] != "eth2" || links[1] != "wg0" {
		t.Errorf("Links(tenant) = %v, %v, want [eth2 wg0]", links, err)
	}
}

// An endpoint is an address and a port, of IPv6 in brackets, or a host name of RFC 1123 labels
// and a port, the port from 1 to 65535.
func TestValidEndpoint(t *testing.T) {
	for e, want := range map[string]bool{
		"[2001:db8::1]:51820": true,
		"192.0.2.1:65535":     true,
		"vpn.example.org:1":   true,
		"a:51820":             true,
		"[2001:db8::1]:0":     false,
		"[fe80::1%eth0]:1":    false,
		"[2001:db8::1:51820":  false,
		"2001:db8::1:51820":   false,
		"vpn..example.org:1":  false,
		"-vpn.example.org:1":  false,
		"vpn.example.org:01":  false,
		"vpn.example.org:":    false,
		"vpn.example.org":     false,
		"vpn_1.example.org:1": false,
	} {
		if got := validEndpoint(e); got != want {
			t.Errorf("validEndpoint(%q) = %v, want %v", e, got, want)
		}
	}
}
