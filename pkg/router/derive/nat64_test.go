package derive

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A NAT64 takes its prefix and its pool entries with their host bits cleared, as the rules
// compare them.
func TestNAT64Masks(t *testing.T) {
	got, err := nat64Pools([]Named[NAT64Spec]{{Name: "edge", Spec: NAT64Spec{Prefix: "64:ff9b::1/96", Pool4: []PoolEntry{{Prefix: "192.0.2.9/30", Ports: "61001-65535", Protocols: []string{"tcp"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if p, e := got[0].Prefix.String(), got[0].Pool4[0].Prefix.String(); p != "64:ff9b::/96" || e != "192.0.2.8/30" {
		t.Errorf("pool6 %s, pool4 %s, want 64:ff9b::/96 and 192.0.2.8/30", p, e)
	}
}

// A pool entry takes the protocols Jool takes, in Jool's order, whatever else the document
// names; an entry of none of them is an internal error, not an entry the pool drops.
func TestNAT64Protocols(t *testing.T) {
	pools := func(protocols ...string) ([]NAT64Instance, error) {
		return nat64Pools([]Named[NAT64Spec]{{Name: "edge", Spec: NAT64Spec{Prefix: "64:ff9b::/96", Pool4: []PoolEntry{{Prefix: "192.0.2.8/30", Ports: "61001-65535", Protocols: protocols}}}}})
	}
	got, err := pools("icmp", "tcp\", \"mark\": \"1", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tcp", "icmp"}; !slices.Equal(got[0].Pool4[0].Protocols, want) {
		t.Errorf("protocols = %v, want %v", got[0].Pool4[0].Protocols, want)
	}
	const refusal = "internal error: NAT64 edge: pool entry 1 names no protocol Jool takes"
	if _, err := pools("tcp\", \"mark\": \"1"); err == nil || !strings.HasPrefix(err.Error(), refusal) {
		t.Errorf("err = %v, want %q", err, refusal)
	}
}

// Each NAT64 gets the /30 of its link from the hash of its name alone, so it keeps the /30 while
// other NAT64s come and go, and the table of its VRF; an explicit pool's prefixes are its pools,
// each once, and an empty pool4 has none.
func TestNAT64Instances(t *testing.T) {
	edge := routerDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp", "udp"]}, {"prefix": "192.0.2.8/30", "ports": "1-1000", "protocols": ["icmp"]}]}`)
	guest := routerDoc(t, "NAT64", "guest", `{"vrf": "guest", "prefix": "64:ff9b::/96", "pool4": []}`)
	instances := func(nat64 ...any) []NAT64Instance {
		t.Helper()
		m, err := Build(map[string]any{
			"NAT64":   nat64,
			"VRF":     []any{routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3"]}`)},
			"Network": []any{document(t, "Network", "40-guest", `{"match": {"name": "eth3"}}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := m.NAT64Instances()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	p := netip.MustParsePrefix
	want := []NAT64Instance{
		{Name: "edge", VRF: DefaultVRF, Table: 254, Prefix: p("64:ff9b::/96"), Outer: p("169.254.96.241/30"), Inner: p("169.254.96.242/30"), Pools: []netip.Prefix{p("192.0.2.8/30")},
			Pool4: []PoolRange{{Prefix: p("192.0.2.8/30"), First: 61001, Last: 65535, Protocols: []string{"tcp", "udp"}}, {Prefix: p("192.0.2.8/30"), First: 1, Last: 1000, Protocols: []string{"icmp"}}}},
		{Name: "guest", VRF: "guest", Table: 300, Prefix: p("64:ff9b::/96"), Outer: p("169.254.111.205/30"), Inner: p("169.254.111.206/30")},
	}
	if got := instances(edge, guest); !reflect.DeepEqual(got, want) {
		t.Errorf("instances:\n%+v\nwant:\n%+v", got, want)
	}
	if got := instances(guest); !reflect.DeepEqual(got, want[1:]) {
		t.Errorf("guest alone:\n%+v\nwant:\n%+v", got, want[1:])
	}
}
