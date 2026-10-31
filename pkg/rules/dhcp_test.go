package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// The DHCPSubnet rules accept an office's subnet with pools, reservations, options and PXE files
// and a subnet of reservations alone, and refuse a subnet of IPv6, of more than 30 bits or of
// addresses no server leases, a pool, reservation or gateway outside the subnet, a DNS server of
// IPv6, two reservations of one MAC address or of one address, a boot of no file, a boot server
// of IPv6 and BIOS or UEFI files without a boot server. An iPXE script of an HTTP URL needs no
// server.
func TestDHCPSubnetRules(t *testing.T) {
	set, doc := routerRules(t)
	subnet := func(name, spec string) config.Document { return doc("DHCPSubnet", name, spec) }
	office := subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24",
		"pools": [{"first": "192.0.2.100", "last": "192.0.2.199"}],
		"gateway": "192.0.2.1", "dns": ["192.0.2.1"], "domain": "lan.example.org", "leaseTime": "1d",
		"reservations": [{"mac": "52:54:00:12:34:56", "address": "192.0.2.10", "hostname": "printer"}],
		"boot": {"server": "192.0.2.5", "bios": "undionly.kpxe", "uefi": "ipxe.efi", "ipxe": "http://192.0.2.5/boot.ipxe"}}`)
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an office", []config.Document{office}, nil},
		{"reservations alone", []config.Document{office, subnet("lab", `{"interfaces": ["lab0"], "subnet": "198.51.100.0/30",
			"reservations": [{"mac": "52:54:00:00:00:01", "address": "198.51.100.2"}]}`)}, nil},
		{"an IPv6 subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "2001:db8:1::/64"}`)},
			[]string{`DHCPSubnet "office" spec.subnet: the subnet 2001:db8:1::/64 is no IPv4 range of 30 bits at most of addresses a DHCP server leases`}},
		{"a subnet of 31 bits", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/31"}`)},
			[]string{`DHCPSubnet "office" spec.subnet: the subnet 192.0.2.0/31 is no IPv4 range of 30 bits at most of addresses a DHCP server leases`}},
		{"a subnet of loopback addresses", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "127.0.0.0/24"}`)},
			[]string{`DHCPSubnet "office" spec.subnet: the subnet 127.0.0.0/24 is no IPv4 range of 30 bits at most of addresses a DHCP server leases`}},
		{"a pool outside the subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/25",
			"pools": [{"first": "192.0.2.100", "last": "192.0.2.199"}]}`)},
			[]string{"DHCPSubnet office has a pool outside its subnet"}},
		{"a pool of IPv6", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24",
			"pools": [{"first": "2001:db8::1", "last": "2001:db8::9"}]}`)},
			[]string{"DHCPSubnet office has a pool outside its subnet"}},
		{"a reservation outside the subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24",
			"reservations": [{"mac": "52:54:00:12:34:56", "address": "198.51.100.10"}]}`)},
			[]string{"DHCPSubnet office has a reservation outside its subnet"}},
		{"a gateway outside the subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "gateway": "198.51.100.1"}`)},
			[]string{"DHCPSubnet office has a gateway outside its subnet"}},
		{"a DNS server of IPv6", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "dns": ["192.0.2.1", "2001:db8::53"]}`)},
			[]string{`DHCPSubnet "office" spec.dns: the DNS server 2001:db8::53 is no IPv4 address`}},
		{"one MAC address twice", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24",
			"reservations": [{"mac": "52:54:00:12:34:56", "address": "192.0.2.10"}, {"mac": "52:54:00:12:34:56", "address": "192.0.2.11"}]}`)},
			[]string{`DHCPSubnet "office" spec.reservations: the MAC address 52:54:00:12:34:56 has more than one reservation`}},
		{"one address twice", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24",
			"reservations": [{"mac": "52:54:00:12:34:56", "address": "192.0.2.10"}, {"mac": "52:54:00:12:34:57", "address": "192.0.2.10"}]}`)},
			[]string{`DHCPSubnet "office" spec.reservations: the address 192.0.2.10 is reserved more than once`}},
		{"a boot of no file", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": {"server": "192.0.2.5"}}`)},
			[]string{`DHCPSubnet "office" spec.boot: boot names no file; give bios, uefi or ipxe, or leave boot out`}},
		{"a boot server of IPv6", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": {"server": "2001:db8::5", "uefi": "ipxe.efi"}}`)},
			[]string{`DHCPSubnet "office" spec.boot: boot's server is no IPv4 address`}},
		{"an iPXE script without a server", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": {"ipxe": "http://192.0.2.5/boot.ipxe"}}`)}, nil},
		{"a BIOS file without a server", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": {"bios": "undionly.kpxe", "ipxe": "http://192.0.2.5/boot.ipxe"}}`)},
			[]string{`DHCPSubnet "office" spec.boot: boot's bios and uefi files load over TFTP from boot's server; give server`}},
		{"a UEFI file without a server", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": {"uefi": "ipxe.efi"}}`)},
			[]string{`DHCPSubnet "office" spec.boot: boot's bios and uefi files load over TFTP from boot's server; give server`}},
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

// The DHCPSubnet schema takes links by name alone, at least one, so the server answers only on
// links a document names; it refuses a glob.
func TestDHCPSubnetSchema(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		interfaces string
		ok         bool
	}{
		{`["lan0"]`, true},
		{`["lan0", "vlan20.1"]`, true},
		{`[]`, false},
		{`["lan*"]`, false},
		{`["*"]`, false},
		{`["lan0x123456789ab"]`, false},
	} {
		d := builtinDoc(t, config.RouterAPI, "DHCPSubnet", "office", `{"interfaces": `+tc.interfaces+`, "subnet": "192.0.2.0/24"}`)
		if err := s.Validate(d.Object); (err == nil) != tc.ok {
			t.Errorf("interfaces %s: err = %v, want accepted %v", tc.interfaces, err, tc.ok)
		}
	}
}
