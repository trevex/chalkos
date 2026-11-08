package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// replaced is docs with each of with in place of the document of its kind and name, or added.
func replaced(docs []config.Document, with ...config.Document) []config.Document {
	out := slices.Clone(docs)
	for _, w := range with {
		i := slices.IndexFunc(out, func(d config.Document) bool { return d.Kind == w.Kind && d.Name == w.Name })
		if i < 0 {
			out = append(out, w)
		} else {
			out[i] = w
		}
	}
	return out
}

// wantFailures evaluates the rules over docs and wants the failures' messages want, in order.
func wantFailures(t *testing.T, set *Set, docs []config.Document, want []string) {
	t.Helper()
	failures, err := set.Evaluate(Input{Documents: config.Redact(docs), Node: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(failures); !slices.Equal(got, want) {
		t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The NextHop rules accept next hops of both families, link-local ones included, and refuse a next
// hop on lo, a table of the kernel's or of a VRF, and an address that is no unicast address: of
// neither family unspecified, loopback or multicast, not IPv4's broadcast and no IPv4-mapped IPv6
// address, which is no IPv6 neighbour on a link.
func TestNextHopRules(t *testing.T) {
	set, doc := routerRules(t)
	hop := func(name, spec string) config.Document { return doc("NextHop", name, spec) }
	base := []config.Document{
		doc("Router", "router", `{"management": {"interfaces": ["lan0"]}}`),
		doc("VRF", "transit", `{"table": 100, "interfaces": ["vt0"]}`),
		hop("b4", `{"address": "198.51.100.130", "interface": "wanb0", "table": 200}`),
		hop("b6", `{"address": "2001:db8:b::2", "interface": "wanb0", "table": 201}`),
	}
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"next hops of both families", base, nil},
		{"link-local next hops", replaced(base,
			hop("ll6", `{"address": "fe80::1", "interface": "wanb0", "table": 202}`),
			hop("ll4", `{"address": "169.254.0.1", "interface": "wanb0", "table": 203}`)), nil},
		{"a next hop on lo", replaced(base, hop("b4", `{"address": "198.51.100.130", "interface": "lo", "table": 200}`)),
			[]string{`NextHop/b4: spec.interface: the interface is lo, which no next hop is on; name the link the next hop is on`}},
		{"a next hop in the default table", replaced(base, hop("b4", `{"address": "198.51.100.130", "interface": "wanb0", "table": 253}`)),
			[]string{`NextHop/b4: spec.table: the table is 253, 254 or 255, the kernel's own; give the NextHop a table of its own`}},
		{"a next hop in a VRF's table", replaced(base, hop("b4", `{"address": "198.51.100.130", "interface": "wanb0", "table": 100}`)),
			[]string{`NextHop/b4: spec.table: the table is that of VRF transit; give the NextHop a table of its own`}},
	} {
		t.Run(tc.name, func(t *testing.T) { wantFailures(t, set, tc.docs, tc.want) })
	}
	for _, address := range []string{"0.0.0.0", "::", "127.0.0.1", "127.1.2.3", "::1", "224.0.0.1", "239.255.255.250", "ff02::1", "255.255.255.255", "::ffff:198.51.100.130", "::ffff:127.0.0.1"} {
		t.Run("a next hop of "+address, func(t *testing.T) {
			docs := replaced(base, hop("spare", `{"address": "`+address+`", "interface": "wanb0", "table": 204}`))
			wantFailures(t, set, docs, []string{`NextHop/spare: spec.address: the next hop ` + address + ` is no unicast address of a router, or is an IPv4-mapped IPv6 address; give the router's address, an IPv4 one as IPv4`})
		})
	}
}
