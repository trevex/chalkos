package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// The DHCPSubnet rules take an IPv6 subnet on one link with pools, a reservation, DNS servers, a
// domain and delegated prefixes, and one of delegated prefixes alone, and refuse an IPv6 subnet of
// more than 120 bits or of link-local addresses, one on two links, an IPv6 subnet's gateway and
// boot files, an IPv4 DNS server of an IPv6 subnet, delegated prefixes of an IPv4 subnet, of an
// IPv4 range and of a length no longer than their range.
func TestDHCPSubnetIPv6Rules(t *testing.T) {
	set, doc := routerRules(t)
	subnet := func(name, spec string) config.Document { return doc("DHCPSubnet", name, spec) }
	office := subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64",
		"pools": [{"first": "2001:db8:1::100", "last": "2001:db8:1::1ff"}],
		"dns": ["2001:db8:1::1"], "domain": "lan.example.org",
		"reservations": [{"mac": "52:54:00:12:34:56", "address": "2001:db8:1::10"}],
		"prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 64}}`)
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an office", []config.Document{office}, nil},
		{"delegated prefixes alone", []config.Document{subnet("cpe", `{"interfaces": ["lan1"], "subnet": "2001:db8:2::/64", "prefixDelegation": {"prefix": "2001:db8:200::/48", "length": 56}}`)}, nil},
		{"an IPv6 subnet of 121 bits", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/121"}`)},
			[]string{`DHCPSubnet "office6" spec.subnet: the subnet 2001:db8:1::/121 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"a link-local subnet", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "fe80::/64"}`)},
			[]string{`DHCPSubnet "office6" spec.subnet: the subnet fe80::/64 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"two links", []config.Document{subnet("office6", `{"interfaces": ["lan0", "lan1"], "subnet": "2001:db8:1::/64"}`)},
			[]string{"DHCPSubnet office6 serves an IPv6 subnet on more than one link; give each link a DHCPSubnet of its own"}},
		{"a gateway", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "gateway": "2001:db8:1::1"}`)},
			[]string{"DHCPSubnet office6 gives a gateway to an IPv6 subnet, whose clients learn their routers from router advertisements"}},
		{"boot files", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "boot": {"ipxe": "http://192.0.2.5/boot.ipxe"}}`)},
			[]string{"DHCPSubnet office6 gives PXE boot files to an IPv6 subnet"}},
		{"an IPv4 DNS server", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "dns": ["192.0.2.1"]}`)},
			[]string{"DHCPSubnet office6 has a DNS server of another family than its subnet"}},
		{"an IPv6 DNS server of an IPv4 subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "dns": ["2001:db8::53"]}`)},
			[]string{"DHCPSubnet office has a DNS server of another family than its subnet"}},
		{"delegation in IPv4", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 64}}`)},
			[]string{"DHCPSubnet office delegates prefixes in an IPv4 subnet"}},
		{"delegation of an IPv4 range", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "prefixDelegation": {"prefix": "198.51.100.0/24", "length": 28}}`)},
			[]string{`DHCPSubnet "office6" spec.prefixDelegation: prefixDelegation's prefix 198.51.100.0/24 is no IPv6 range shorter than 28 bits`}},
		{"delegation of the range's length", []config.Document{subnet("office6", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64", "prefixDelegation": {"prefix": "2001:db8:100::/56", "length": 56}}`)},
			[]string{`DHCPSubnet "office6" spec.prefixDelegation: prefixDelegation's prefix 2001:db8:100::/56 is no IPv6 range shorter than 56 bits`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}
