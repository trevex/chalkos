package rules

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// The DHCPSubnet rules accept an office's subnet with pools, reservations, options and PXE files
// and a subnet of reservations alone, and refuse a subnet of more than 30 bits or of addresses no
// server leases; a pool outside the subnet, that runs backwards, that shares addresses with
// another, or that holds the subnet's network or broadcast address, the gateway, a DNS server or
// boot's server; a reservation outside the subnet, in a pool, at the network or broadcast address
// or of a server's address; a gateway outside the subnet or at its network or broadcast address;
// a DNS server of IPv6 in an IPv4 subnet; a boot of no file, a boot server of IPv6 and BIOS or UEFI
// files without a boot server. An iPXE script of an HTTP URL needs no server.
func TestDHCPSubnetRules(t *testing.T) {
	set, doc := routerRules(t)
	subnet := func(name, spec string) config.Document { return doc("DHCPSubnet", name, spec) }
	office := func(more string) []config.Document {
		return []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"`+more+`}`)}
	}
	pools := func(p ...string) string {
		var l []string
		for i := 0; i+1 < len(p); i += 2 {
			l = append(l, `{"first": "`+p[i]+`", "last": "`+p[i+1]+`"}`)
		}
		return `, "pools": [` + strings.Join(l, ", ") + `]`
	}
	reserve := func(address string) string {
		return `, "reservations": [{"mac": "52:54:00:12:34:56", "address": "` + address + `"}]`
	}
	const at = `DHCPSubnet "office" spec: `
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an office", office(pools("192.0.2.100", "192.0.2.199") + `, "gateway": "192.0.2.1", "dns": ["192.0.2.1", "198.51.100.53"], "domain": "lan.example.org", "leaseTime": "1d",
			"reservations": [{"mac": "52:54:00:12:34:56", "address": "192.0.2.10", "hostname": "printer"}],
			"boot": {"server": "192.0.2.5", "bios": "undionly.kpxe", "uefi": "ipxe.efi", "ipxe": "http://192.0.2.5/boot.ipxe"}`), nil},
		{"reservations alone", []config.Document{subnet("lab", `{"interfaces": ["lab0"], "subnet": "198.51.100.0/30",
			"reservations": [{"mac": "52:54:00:00:00:01", "address": "198.51.100.2"}]}`)}, nil},
		{"a subnet of 31 bits", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/31"}`)},
			[]string{`DHCPSubnet "office" spec.subnet: the subnet 192.0.2.0/31 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"a subnet of loopback addresses", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "127.0.0.0/24"}`)},
			[]string{`DHCPSubnet "office" spec.subnet: the subnet 127.0.0.0/24 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		// The rule compares ranges, not first addresses: 192.0.0.0/2 starts outside 224.0.0.0/3
		// and holds it.
		{"a subnet over the multicast addresses", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.0.0/2"}`)},
			[]string{`DHCPSubnet "office" spec.subnet: the subnet 192.0.0.0/2 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases`}},
		{"a subnet beside the loopback addresses", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "128.0.0.0/30"}`)}, nil},
		{"a pool outside the subnet", []config.Document{subnet("office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/25"`+pools("192.0.2.100", "192.0.2.199")+`}`)},
			[]string{at + "the pool 192.0.2.100-192.0.2.199 lies outside the subnet 192.0.2.0/25"}},
		{"a pool of IPv6", office(pools("2001:db8::1", "2001:db8::9")), []string{at + "the pool 2001:db8::1-2001:db8::9 lies outside the subnet 192.0.2.0/24"}},
		{"a pool backwards", office(pools("192.0.2.199", "192.0.2.100")), []string{at + "the pool 192.0.2.199-192.0.2.100 ends before it starts"}},
		{"a pool of one address", office(pools("192.0.2.100", "192.0.2.100")), nil},
		{"pools that share an address", office(pools("192.0.2.100", "192.0.2.150", "192.0.2.150", "192.0.2.199")),
			[]string{at + "the pool 192.0.2.100-192.0.2.150 shares addresses with another pool"}},
		{"a pool within another", office(pools("192.0.2.100", "192.0.2.199", "192.0.2.120", "192.0.2.130")),
			[]string{at + "the pool 192.0.2.100-192.0.2.199 shares addresses with another pool"}},
		{"one pool twice", office(pools("192.0.2.100", "192.0.2.199", "192.0.2.100", "192.0.2.199")),
			[]string{at + "the pool 192.0.2.100-192.0.2.199 shares addresses with another pool"}},
		{"pools side by side", office(pools("192.0.2.150", "192.0.2.199", "192.0.2.100", "192.0.2.149")), nil},
		{"a pool of the network address", office(pools("192.0.2.0", "192.0.2.10")),
			[]string{at + "the pool 192.0.2.0-192.0.2.10 holds 192.0.2.0, the network address of the subnet 192.0.2.0/24"}},
		{"a pool of the broadcast address", office(pools("192.0.2.200", "192.0.2.255")),
			[]string{at + "the pool 192.0.2.200-192.0.2.255 holds 192.0.2.255, the broadcast address of the subnet 192.0.2.0/24"}},
		{"a pool of every other address", office(pools("192.0.2.1", "192.0.2.254")), nil},
		{"a pool of the gateway", office(pools("192.0.2.100", "192.0.2.199") + `, "gateway": "192.0.2.150"`),
			[]string{at + "the pool 192.0.2.100-192.0.2.199 holds 192.0.2.150, the gateway"}},
		{"a pool of a DNS server", office(pools("192.0.2.100", "192.0.2.199") + `, "dns": ["198.51.100.53", "192.0.2.153"]`),
			[]string{at + "the pool 192.0.2.100-192.0.2.199 holds 192.0.2.153, a DNS server"}},
		{"a pool of boot's server", office(pools("192.0.2.100", "192.0.2.199") + `, "boot": {"server": "192.0.2.199", "bios": "undionly.kpxe"}`),
			[]string{at + "the pool 192.0.2.100-192.0.2.199 holds 192.0.2.199, boot's server"}},
		{"a reservation outside the subnet", office(reserve("198.51.100.10")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 198.51.100.10, lies outside the subnet 192.0.2.0/24"}},
		{"a reservation in a pool", office(pools("192.0.2.100", "192.0.2.199") + reserve("192.0.2.120")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.120, lies in the pool 192.0.2.100-192.0.2.199; reserve addresses outside the pools"}},
		{"a reservation at a pool's last address", office(pools("192.0.2.100", "192.0.2.199") + reserve("192.0.2.199")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.199, lies in the pool 192.0.2.100-192.0.2.199; reserve addresses outside the pools"}},
		{"a reservation beside a pool", office(pools("192.0.2.100", "192.0.2.199") + reserve("192.0.2.200")), nil},
		{"a reservation of the network address", office(reserve("192.0.2.0")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.0, is the network address of the subnet 192.0.2.0/24"}},
		{"a reservation of the broadcast address", office(reserve("192.0.2.255")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.255, is the broadcast address of the subnet 192.0.2.0/24"}},
		{"the gateway reserved", office(`, "gateway": "192.0.2.254"` + reserve("192.0.2.254")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.254, is the gateway"}},
		{"a DNS server reserved", office(`, "dns": ["192.0.2.53"]` + reserve("192.0.2.53")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.53, is a DNS server"}},
		{"boot's server reserved", office(`, "boot": {"server": "192.0.2.5", "bios": "undionly.kpxe"}` + reserve("192.0.2.5")),
			[]string{at + "the reservation of 52:54:00:12:34:56, 192.0.2.5, is boot's server"}},
		{"a gateway outside the subnet", office(`, "gateway": "198.51.100.1"`), []string{at + "the gateway 198.51.100.1 lies outside the subnet 192.0.2.0/24"}},
		{"a gateway at the network address", office(`, "gateway": "192.0.2.0"`),
			[]string{at + "the gateway, 192.0.2.0, is the network address of the subnet 192.0.2.0/24"}},
		{"a gateway at the broadcast address", office(`, "gateway": "192.0.2.255"`),
			[]string{at + "the gateway, 192.0.2.255, is the broadcast address of the subnet 192.0.2.0/24"}},
		{"a DNS server of IPv6", office(`, "dns": ["192.0.2.1", "2001:db8::53"]`), []string{at + "the DNS server 2001:db8::53 is no IPv4 address"}},
		{"an IPv4-mapped DNS server", office(`, "dns": ["::ffff:192.0.2.1"]`), []string{at + "the DNS server ::ffff:192.0.2.1 is no IPv4 address"}},
		{"a boot of no file", office(`, "boot": {"server": "192.0.2.5"}`),
			[]string{`DHCPSubnet "office" spec.boot: boot names no file; give bios, uefi or ipxe, or leave boot out`}},
		{"a boot server of IPv6", office(`, "boot": {"server": "2001:db8::5", "uefi": "ipxe.efi"}`),
			[]string{`DHCPSubnet "office" spec.boot: boot's server is no IPv4 address`}},
		{"an iPXE script without a server", office(`, "boot": {"ipxe": "http://192.0.2.5/boot.ipxe"}`), nil},
		{"a BIOS file without a server", office(`, "boot": {"bios": "undionly.kpxe", "ipxe": "http://192.0.2.5/boot.ipxe"}`),
			[]string{`DHCPSubnet "office" spec.boot: boot's bios and uefi files load over TFTP from boot's server; give server`}},
		{"a UEFI file without a server", office(`, "boot": {"uefi": "ipxe.efi"}`),
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

// The rules of a subnet's spec compare its pools with each other, its reservations with its pools
// and both with its servers within chalkd's runtime cost limit, and so do their messages, at the
// most pools, reservations and DNS servers the schema takes; it takes no more.
func TestDHCPSubnetManyRanges(t *testing.T) {
	set, doc := routerRules(t)
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	const maxPools, maxReservations, maxDNS = 32, 512, 16
	// A subnet of n pools, m reservations and d DNS servers, each list's last item at last.
	subnet := func(n, m, d int, last ...string) config.Document {
		var pools, reservations, dns []string
		for i := range n {
			pools = append(pools, fmt.Sprintf(`{"first": "10.%d.0.1", "last": "10.%d.0.254"}`, i, i))
		}
		for i := range m {
			reservations = append(reservations, fmt.Sprintf(`{"mac": "52:54:00:00:%02x:%02x", "address": "10.%d.%d.1", "hostname": "host-%d"}`, i/256, i%256, 100+i/256, i%256, i))
		}
		for i := range d {
			dns = append(dns, fmt.Sprintf(`"10.200.%d.53"`, i))
		}
		if len(last) == 3 {
			pools[n-1], reservations[m-1], dns[d-1] = last[0], last[1], last[2]
		}
		return doc("DHCPSubnet", "office", `{"interfaces": ["lan0"], "subnet": "10.0.0.0/8", "gateway": "10.255.255.254", "boot": {"server": "10.255.255.253", "bios": "undionly.kpxe"}, "pools": [`+
			strings.Join(pools, ", ")+`], "reservations": [`+strings.Join(reservations, ", ")+`], "dns": [`+strings.Join(dns, ", ")+`]}`)
	}
	const pool = `{"first": "10.31.0.1", "last": "10.31.0.254"}`
	const at = `DHCPSubnet "office" spec: `
	for _, tc := range []struct {
		name string
		doc  config.Document
		want []string
	}{
		{"the most of each", subnet(maxPools, maxReservations, maxDNS), nil},
		{"the last reservation in the last pool", subnet(maxPools, maxReservations, maxDNS, pool, `{"mac": "52:54:00:00:01:ff", "address": "10.31.0.10"}`, `"10.200.15.53"`),
			[]string{at + "the reservation of 52:54:00:00:01:ff, 10.31.0.10, lies in the pool 10.31.0.1-10.31.0.254; reserve addresses outside the pools"}},
		{"the last DNS server in the last pool", subnet(maxPools, maxReservations, maxDNS, pool, `{"mac": "52:54:00:00:01:ff", "address": "10.101.255.1"}`, `"10.31.0.53"`),
			[]string{at + "the pool 10.31.0.1-10.31.0.254 holds 10.31.0.53, a DNS server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Validate(tc.doc.Object); err != nil {
				t.Fatal(err)
			}
			failures, err := set.Evaluate(Input{Documents: []config.Document{tc.doc}, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
	for _, more := range []config.Document{subnet(maxPools+1, 1, 1), subnet(1, maxReservations+1, 1), subnet(1, 1, maxDNS+1)} {
		if err := s.Validate(more.Object); err == nil {
			t.Errorf("the schema takes %v", more.Object["spec"])
		}
	}
}

// A lease lasts 4294967295 seconds at most, the longest Kea takes: 49710 days, while 999999
// hours, the most the schema takes of hours, lie below it.
func TestDHCPSubnetLeaseTime(t *testing.T) {
	set, doc := routerRules(t)
	for leaseTime, want := range map[string][]string{
		"12h":     nil,
		"999999s": nil,
		"999999h": nil,
		"49710d":  nil,
		"49711d":  {`DHCPSubnet "office" spec.leaseTime: a lease of 49711d is longer than 4294967295 seconds, the longest Kea takes`},
		"999999d": {`DHCPSubnet "office" spec.leaseTime: a lease of 999999d is longer than 4294967295 seconds, the longest Kea takes`},
	} {
		d := doc("DHCPSubnet", "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "leaseTime": "`+leaseTime+`"}`)
		failures, err := set.Evaluate(Input{Documents: []config.Document{d}, Node: "r1"})
		if err != nil {
			t.Fatal(err)
		}
		if got := messages(failures); !slices.Equal(got, want) {
			t.Errorf("leaseTime %s: failures = %q, want %q", leaseTime, got, want)
		}
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
