// Package derivetest holds the fixtures of the tests of a router's config: its documents as the
// router outputs and the validator router take them, by kind, and the router's documents of the
// scenarios both test.
package derivetest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// routerAPI is the API version of the router kinds. It mirrors derive.API, which derivetest does
// not import, so that derive's own tests share it.
const routerAPI = "router.chalkos.dev/v1alpha1"

// Doc is a networkd document of kind called name, with spec in JSON, its numbers exact.
func Doc(t testing.TB, kind, name, spec string) any {
	t.Helper()
	return withSpec(t, config.NetworkdAPI, kind, name, spec)
}

// RouterDoc is a document of a router kind called name, with spec in JSON, its numbers exact.
func RouterDoc(t testing.TB, kind, name, spec string) any {
	t.Helper()
	return withSpec(t, routerAPI, kind, name, spec)
}

func withSpec(t testing.TB, apiVersion, kind, name, spec string) any {
	t.Helper()
	var s any
	dec := json.NewDecoder(strings.NewReader(spec))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
}

// SubnetDoc is a DHCPSubnet document of the spec given, which the schema's defaults complete.
func SubnetDoc(t testing.TB, name, spec string) any {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		t.Fatal(err)
	}
	full := map[string]any{"pools": []any{}, "gateway": nil, "dns": []any{}, "domain": nil, "leaseTime": "12h", "reservations": []any{}, "boot": nil, "prefixDelegation": nil}
	for k, v := range s {
		full[k] = v
	}
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	return RouterDoc(t, "DHCPSubnet", name, string(data))
}

// KeaDocs are a LAN router's documents with the DHCPSubnet documents given: lan0 holds
// 192.0.2.1/24 and 192.0.2.30/24, lan1 198.51.100.1/24 and 192.0.2.65/26, vlan20 in the VRF guest
// 192.0.2.1/24 as well and lan2, in the VRF ghost of no VRF document, 203.0.113.1/24; the NAT64
// edge has the link n64-edge.
func KeaDocs(t testing.TB, subnets ...any) map[string][]any {
	t.Helper()
	return map[string][]any{
		"VRF":   {RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["vlan20"]}`)},
		"NAT64": {RouterDoc(t, "NAT64", "edge", `{"vrf": null, "prefix": "64:ff9b::/96", "pool4": []}`)},
		"Network": {
			Doc(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["192.0.2.1/24", "192.0.2.30/24"]}}`),
			Doc(t, "Network", "11-lan1", `{"match": {"name": "lan1"}, "network": {"address": ["198.51.100.1/24", "192.0.2.65/26"]}}`),
			Doc(t, "Network", "12-lan2", `{"match": {"name": "lan2"}, "network": {"vrf": ["ghost"], "address": ["203.0.113.1/24"]}}`),
			Doc(t, "Network", "20-vlan20", `{"match": {"name": "vlan20"}, "network": {"address": ["192.0.2.1/24"]}}`),
		},
		"DHCPSubnet": subnets,
	}
}

// Kea6Docs are a LAN router's documents with the DHCPSubnet documents given: lan0 holds
// 2001:db8:1::1/64 and 2001:db8:5::1/64 and sends router advertisements with the Managed flag,
// lan1 2001:db8:2::1/64 with the OtherInformation flag, lan2 2001:db8:3::1/64 without router
// advertisements, lan3 2001:db8:4::1/64 with them but without either flag, and vlan30 in the VRF
// guest 2001:db8:1::1/64 with the Managed flag as well.
func Kea6Docs(t testing.TB, subnets ...any) map[string][]any {
	t.Helper()
	ra := func(name, link, addresses, flags string) any {
		return Doc(t, "Network", name, `{"match": {"name": "`+link+`"}, "network": {"address": [`+addresses+`], "ipv6SendRA": "yes"}, "ipv6SendRA": `+flags+`}`)
	}
	return map[string][]any{
		"VRF": {RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["vlan30"]}`)},
		"Network": {
			ra("10-lan0", "lan0", `"2001:db8:1::1/64", "2001:db8:5::1/64"`, `{"managed": true}`),
			ra("11-lan1", "lan1", `"2001:db8:2::1/64"`, `[{"managed": "no"}, {"otherInformation": ["off", " 1 "]}]`),
			Doc(t, "Network", "12-lan2", `{"match": {"name": "lan2"}, "network": {"address": ["2001:db8:3::1/64"]}, "ipv6SendRA": {"managed": true}}`),
			ra("13-lan3", "lan3", `"2001:db8:4::1/64"`, `{"managed": "maybe"}`),
			ra("30-vlan30", "vlan30", `"2001:db8:1::1/64"`, `{"managed": 1}`),
		},
		"DHCPSubnet": subnets,
	}
}

// RelayDocs are a router's documents with the DHCPRelay and DHCPSubnet documents given: lan0
// holds 192.0.2.1/24, fe80::1/64 and 2001:db8:1::1/64, lan1 198.51.100.1/24 alone, lan2
// 2001:db8:2::1/64 and 203.0.113.1/24 in the VRF guest, lan3 203.0.113.65/26 in the VRF dmz,
// and lan4 2001:db8:4::1/64 in the VRF dmz with router advertisements of the Managed flag.
func RelayDocs(t testing.TB, relays []any, subnets ...any) map[string][]any {
	t.Helper()
	return map[string][]any{
		"VRF": {
			RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["lan2"]}`),
			RouterDoc(t, "VRF", "dmz", `{"table": 400, "interfaces": ["lan3", "lan4"]}`),
		},
		"Network": {
			Doc(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["192.0.2.1/24", "fe80::1/64", "2001:db8:1::1/64"]}}`),
			Doc(t, "Network", "11-lan1", `{"match": {"name": "lan1"}, "network": {"address": ["198.51.100.1/24"]}}`),
			Doc(t, "Network", "12-lan2", `{"match": {"name": "lan2"}, "network": {"address": ["2001:db8:2::1/64", "203.0.113.1/24"]}}`),
			Doc(t, "Network", "13-lan3", `{"match": {"name": "lan3"}, "network": {"address": ["203.0.113.65/26"]}}`),
			Doc(t, "Network", "14-lan4", `{"match": {"name": "lan4"}, "network": {"address": ["2001:db8:4::1/64"], "ipv6SendRA": true}, "ipv6SendRA": {"managed": true}}`),
		},
		"DHCPRelay":  relays,
		"DHCPSubnet": subnets,
	}
}

// VRFZoneDocs are a router's documents of three VRFs and their zones: guest of eth3 and eth4,
// whose links two zones name, internet of eth1 and tenant of eth2 and swp*, each a zone's VRF,
// and the zone lan of eth5 and vlan*, links of no VRF.
func VRFZoneDocs(t testing.TB) map[string][]any {
	t.Helper()
	zone := func(name, spec string) any { return RouterDoc(t, "FirewallZone", name, spec) }
	return map[string][]any{
		"VRF": {
			RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3", "eth4"]}`),
			RouterDoc(t, "VRF", "internet", `{"table": 100, "interfaces": ["eth1"]}`),
			RouterDoc(t, "VRF", "tenant", `{"table": 200, "interfaces": ["eth2", "swp*"]}`),
		},
		"Network": {
			Doc(t, "Network", "10-mgmt", `{"match": {"name": "eth0 eth5"}}`),
			Doc(t, "Network", "20-internet", `{"match": {"name": "eth1"}}`),
			Doc(t, "Network", "30-tenant", `{"match": {"name": "eth2 swp1 swp2"}}`),
			Doc(t, "Network", "40-guest", `{"match": {"name": "eth3 eth4"}}`),
		},
		"FirewallZone": {
			zone("guest-lan", `{"interfaces": ["eth3"], "vrfs": []}`),
			zone("guest-wan", `{"interfaces": ["eth4"], "vrfs": []}`),
			zone("internet", `{"interfaces": [], "vrfs": ["internet"]}`),
			zone("lan", `{"interfaces": ["eth5", "vlan*"], "vrfs": []}`),
			zone("tenant", `{"interfaces": [], "vrfs": ["tenant"]}`),
		},
	}
}

// PolicyDocs are a router's documents with the PolicyRoute documents given as name and spec: a
// LAN zone on lan0, a VRF transit of table 100 on vt0 and Networks of lan0, wanb0 and vt0.
func PolicyDocs(t testing.TB, routes ...string) map[string][]any {
	t.Helper()
	var docs []any
	for i := 0; i+1 < len(routes); i += 2 {
		docs = append(docs, RouterDoc(t, "PolicyRoute", routes[i], routes[i+1]))
	}
	return map[string][]any{
		"FirewallZone": {RouterDoc(t, "FirewallZone", "lan", `{"interfaces": ["lan0"], "vrfs": []}`)},
		"VRF":          {RouterDoc(t, "VRF", "transit", `{"table": 100, "interfaces": ["vt0"]}`)},
		"Network": {
			Doc(t, "Network", "10-lan", `{"match": {"name": "lan0"}}`),
			Doc(t, "Network", "21-wanb", `{"match": {"name": "wanb0"}}`),
			Doc(t, "Network", "30-transit", `{"match": {"name": "vt0"}}`),
		},
		"PolicyRoute": docs,
	}
}
