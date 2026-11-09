package derive

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// A NAT64 takes its prefix and its pool entries with their host bits cleared, as the rules
// compare them.
func TestNAT64Masks(t *testing.T) {
	got, err := nat64Pools([]Named[NAT64Spec]{{Name: "edge", Spec: NAT64Spec{Prefix: netip.MustParsePrefix("64:ff9b::1/96"), Pool4: []PoolEntry{{Prefix: netip.MustParsePrefix("192.0.2.9/30"), Ports: "61001-65535", Protocols: []string{"tcp"}}}}}})
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
		return nat64Pools([]Named[NAT64Spec]{{Name: "edge", Spec: NAT64Spec{Prefix: netip.MustParsePrefix("64:ff9b::/96"), Pool4: []PoolEntry{{Prefix: netip.MustParsePrefix("192.0.2.8/30"), Ports: "61001-65535", Protocols: protocols}}}}})
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
	edge := derivetest.RouterDoc(t, "NAT64", "edge", `{"prefix": "64:ff9b::/96", "pool4": [{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp", "udp"]}, {"prefix": "192.0.2.8/30", "ports": "1-1000", "protocols": ["icmp"]}]}`)
	guest := derivetest.RouterDoc(t, "NAT64", "guest", `{"vrf": "guest", "prefix": "64:ff9b::/96", "pool4": []}`)
	instances := func(nat64 ...any) []NAT64Instance {
		t.Helper()
		m, err := Build(map[string]any{
			"NAT64":   nat64,
			"VRF":     []any{derivetest.RouterDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["eth3"]}`)},
			"Network": []any{derivetest.Doc(t, "Network", "40-guest", `{"match": {"name": "eth3"}}`)},
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

// The NAT64s refuse what the rules leave to the model, as they would compare the NAT64 documents
// in pairs: two NAT64s of one VRF whose prefixes overlap, two NAT64s whose pools share an address
// and two names whose hashes pick one link.
func TestNAT64Refusals(t *testing.T) {
	nat64 := func(name, spec string) any { return derivetest.RouterDoc(t, "NAT64", name, spec) }
	pool := func(entries string) any { return nat64("edge", `{"prefix": "64:ff9b::/96", "pool4": [`+entries+`]}`) }
	tenant := func(name, prefix string) any {
		return nat64(name, `{"vrf": "tenant", "prefix": "`+prefix+`", "pool4": []}`)
	}
	for name, c := range map[string]struct {
		docs []any
		want string
	}{
		"two names of one link": {[]any{nat64("nat282", `{"prefix": "2001:db8:282::/96", "pool4": []}`), nat64("nat8", `{"prefix": "2001:db8:8::/96", "pool4": []}`)},
			"NAT64/nat8: metadata.name: NAT64 nat8 and NAT64 nat282 both take the link addresses 169.254.120.112/30, which the hashes of their names pick; rename NAT64 nat8"},
		"two NAT64s of one prefix": {[]any{nat64("a", `{"prefix": "64:ff9b::/96", "pool4": []}`), nat64("b", `{"prefix": "64:ff9b::/96", "pool4": []}`)},
			"NAT64/b: spec.prefix: NAT64 b and NAT64 a both translate addresses of 64:ff9b::/96 in VRF default; give each NAT64 of a VRF a prefix of its own"},
		"a prefix within another's": {[]any{nat64("a", `{"prefix": "64:ff9b::/96", "pool4": []}`), nat64("b", `{"prefix": "64:ff9b::/64", "pool4": []}`)},
			"NAT64/b: spec.prefix: NAT64 b and NAT64 a both translate addresses of 64:ff9b::/64 in VRF default; give each NAT64 of a VRF a prefix of its own"},
		"two NAT64s of one prefix in a VRF": {[]any{tenant("a", "64:ff9b::/96"), tenant("b", "64:ff9b::/64")},
			"NAT64/b: spec.prefix: NAT64 b and NAT64 a both translate addresses of 64:ff9b::/64 in VRF tenant; give each NAT64 of a VRF a prefix of its own"},
		"two NAT64s of one pool's address": {[]any{pool(`{"prefix": "192.0.2.8/30", "ports": "61001-65535", "protocols": ["tcp"]}`),
			nat64("lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "192.0.2.10/32", "ports": "1-1000", "protocols": ["udp"]}]}`)},
			"NAT64/lab: spec.pool4[0]: the entry shares addresses with the pool of NAT64 edge; give each NAT64 a pool of its own"},
	} {
		t.Run(name, func(t *testing.T) {
			refused(t, map[string][]any{"NAT64": c.docs, "VRF": {derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100}`)}}, c.want)
		})
	}
}
