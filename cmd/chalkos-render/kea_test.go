package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// keaDocs are a LAN router's documents with the DHCPSubnet documents given: lan0 holds
// 192.0.2.1/24 and 192.0.2.30/24, lan1 198.51.100.1/24 and 192.0.2.65/26, and vlan20 in the VRF
// guest 192.0.2.1/24 as well; the NAT64 edge has the link n64-edge.
func keaDocs(t *testing.T, subnets ...any) map[string][]any {
	t.Helper()
	return map[string][]any{
		"VRF":   {routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["vlan20"]}`)},
		"NAT64": {routerDoc(t, "NAT64", "edge", `{"vrf": null, "prefix": "64:ff9b::/96", "pool4": []}`)},
		"Network": {
			document(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["192.0.2.1/24", "192.0.2.30/24"]}}`),
			document(t, "Network", "11-lan1", `{"match": {"name": "lan1"}, "network": {"address": ["198.51.100.1/24", "192.0.2.65/26"]}}`),
			document(t, "Network", "20-vlan20", `{"match": {"name": "vlan20"}, "network": {"address": ["192.0.2.1/24"]}}`),
		},
		"DHCPSubnet": subnets,
	}
}

// subnetDoc is a DHCPSubnet document of the spec given, which the schema's defaults complete.
func subnetDoc(t *testing.T, name, spec string) any {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		t.Fatal(err)
	}
	full := map[string]any{"pools": []any{}, "gateway": nil, "dns": []any{}, "domain": nil, "leaseTime": "12h", "reservations": []any{}, "boot": nil}
	for k, v := range s {
		full[k] = v
	}
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	return routerDoc(t, "DHCPSubnet", name, string(data))
}

// The renderer refuses what the schema and the rules refuse, should a document bypass them, and
// what spans documents or takes an order of addresses: a subnet's links given by a glob, a VRF's
// device, a NAT64's link, a link of no Network, links of two VRFs, a link without the router's
// address in the subnet, a link of two subnets, subnets that overlap in a VRF, a pool or
// reservation outside the subnet, of its network or broadcast address or of the router's, a pool
// that ends before it starts or overlaps another, a reservation in a pool, and values Kea's config
// would take otherwise.
func TestKeaRefusals(t *testing.T) {
	office := func(more string) any {
		return subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"`+more+`}`)
	}
	pool := func(first, last string) any {
		return office(`, "pools": [{"first": "` + first + `", "last": "` + last + `"}]`)
	}
	reserve := func(mac, address, hostname string) any {
		return office(`, "pools": [{"first": "192.0.2.100", "last": "192.0.2.199"}], "reservations": [{"mac": "` + mac + `", "address": "` + address + `", "hostname": ` + hostname + `}]`)
	}
	for name, c := range map[string]struct {
		subnets []any
		want    string
	}{
		"an IPv6 subnet":          {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "2001:db8::/64"}`)}, `DHCPSubnet office: the subnet "2001:db8::/64" is no IPv4 range of 30 bits at most`},
		"a subnet of 31 bits":     {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/31"}`)}, `the subnet "192.0.2.0/31" is no IPv4 range of 30 bits at most`},
		"a subnet at 0.0.0.0":     {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "0.0.0.0/24"}`)}, "the subnet 0.0.0.0/24 starts at 0.0.0.0, which Kea numbers no subnet by"},
		"no interface":            {[]any{subnetDoc(t, "office", `{"interfaces": [], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet office: interfaces names no interface"},
		"a glob":                  {[]any{subnetDoc(t, "office", `{"interfaces": ["lan*"], "subnet": "192.0.2.0/24"}`)}, `DHCPSubnet office: interfaces: "lan*" is a glob; a subnet names each link it is served on`},
		"a glob of every link":    {[]any{subnetDoc(t, "office", `{"interfaces": ["*"], "subnet": "192.0.2.0/24"}`)}, `DHCPSubnet office: interfaces: "*" is a glob; a subnet names each link it is served on`},
		"a VRF's device":          {[]any{subnetDoc(t, "office", `{"interfaces": ["guest"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet office: guest is the device of VRF guest; name the VRF's links"},
		"a NAT64's link":          {[]any{subnetDoc(t, "office", `{"interfaces": ["n64-edge"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet office: n64-edge is the link of NAT64 edge"},
		"links of two VRFs":       {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0", "vlan20"], "subnet": "192.0.2.0/24"}`)}, "its links are in VRF default and VRF guest; a subnet is served in one VRF"},
		"a link of no Network":    {[]any{subnetDoc(t, "office", `{"interfaces": ["lan9"], "subnet": "192.0.2.0/24"}`)}, "no Network document matches lan9 by Name= alone"},
		"a link without address":  {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "203.0.113.0/24"}`)}, "Network 10-lan0 gives lan0 no static address in the subnet 203.0.113.0/24, which Kea answers its clients from"},
		"a link of two subnets":   {[]any{office(""), subnetDoc(t, "other", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet office and DHCPSubnet other both serve lan0; a link takes one subnet"},
		"overlapping subnets":     {[]any{office(""), subnetDoc(t, "servers", `{"interfaces": ["lan1"], "subnet": "192.0.2.64/26"}`)}, "DHCPSubnet office and DHCPSubnet servers overlap in VRF default: 192.0.2.0/24 and 192.0.2.64/26"},
		"a pool outside":          {[]any{pool("192.0.2.100", "198.51.100.10")}, `the pool's last address, "198.51.100.10", is no address of the subnet 192.0.2.0/24`},
		"a pool of IPv6":          {[]any{pool("2001:db8::1", "2001:db8::9")}, `the pool's first address, "2001:db8::1", is no address of the subnet 192.0.2.0/24`},
		"a pool backwards":        {[]any{pool("192.0.2.199", "192.0.2.100")}, "the pool 192.0.2.199-192.0.2.100 ends before it starts"},
		"the network address":     {[]any{pool("192.0.2.0", "192.0.2.10")}, "the pool's first address, 192.0.2.0, is the network or broadcast address of the subnet 192.0.2.0/24"},
		"the broadcast address":   {[]any{pool("192.0.2.200", "192.0.2.255")}, "the pool's last address, 192.0.2.255, is the network or broadcast address of the subnet 192.0.2.0/24"},
		"the router's address":    {[]any{pool("192.0.2.1", "192.0.2.10")}, "the pool's first address, 192.0.2.1, is the router's own address on the subnet's links"},
		"a pool of the router's":  {[]any{pool("192.0.2.10", "192.0.2.50")}, "the pool 192.0.2.10-192.0.2.50 holds 192.0.2.30, the router's own address on the subnet's links"},
		"overlapping pools":       {[]any{office(`, "pools": [{"first": "192.0.2.100", "last": "192.0.2.150"}, {"first": "192.0.2.150", "last": "192.0.2.199"}]`)}, "the pools 192.0.2.100-192.0.2.150 and 192.0.2.150-192.0.2.199 overlap"},
		"a reservation in a pool": {[]any{reserve("52:54:00:12:34:56", "192.0.2.120", "null")}, "the reservation of 52:54:00:12:34:56, 192.0.2.120, lies in the pool 192.0.2.100-192.0.2.199; reserve addresses outside the pools"},
		"a reservation outside":   {[]any{reserve("52:54:00:12:34:56", "198.51.100.10", "null")}, `the reservation of 52:54:00:12:34:56, "198.51.100.10", is no address of the subnet 192.0.2.0/24`},
		"the router reserved":     {[]any{reserve("52:54:00:12:34:56", "192.0.2.30", "null")}, "the reservation of 52:54:00:12:34:56, 192.0.2.30, is the router's own address on the subnet's links"},
		"a MAC in capitals":       {[]any{reserve("52:54:00:AB:CD:EF", "192.0.2.10", "null")}, `"52:54:00:AB:CD:EF" is no MAC address in lower case`},
		"a gateway outside":       {[]any{office(`, "gateway": "203.0.113.1"`)}, `the gateway "203.0.113.1" is no address of the subnet 192.0.2.0/24`},
		"a DNS server of IPv6":    {[]any{office(`, "dns": ["2001:db8::53"]`)}, `the DNS server "2001:db8::53" is no IPv4 address`},
		"a domain in capitals":    {[]any{office(`, "domain": "LAN.example.org"`)}, `"LAN.example.org" is no domain`},
		"a lease of 999999 days":  {[]any{office(`, "leaseTime": "999999d"`)}, "leaseTime 999999d is longer than 4294967295 seconds, the longest Kea takes"},
		"a lease with a space":    {[]any{office(`, "leaseTime": "12 h"`)}, `leaseTime "12 h" is no number and s, m, h or d`},
		"a boot of no file":       {[]any{office(`, "boot": {"server": "192.0.2.5", "bios": null, "uefi": null, "ipxe": null}`)}, "boot names no file"},
		"a boot server of IPv6":   {[]any{office(`, "boot": {"server": "2001:db8::5", "bios": null, "uefi": "ipxe.efi", "ipxe": null}`)}, `boot's server "2001:db8::5" is no IPv4 address`},
		// Values Kea's config would take as something else.
		"a host name of a quote":  {[]any{reserve("52:54:00:12:34:56", "192.0.2.10", `"printer\"x"`)}, `the reservation of 52:54:00:12:34:56: "printer\"x" is no host name`},
		"a boot file of a space":  {[]any{office(`, "boot": {"server": null, "bios": "undionly.kpxe x", "uefi": null, "ipxe": null}`)}, `boot's file "undionly.kpxe x" is no file of 1 to 127 printable characters without spaces`},
		"an interface of a quote": {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0\""], "subnet": "192.0.2.0/24"}`)}, `interfaces: "lan0\"" is no interface name`},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"kea"}, keaDocs(t, c.subnets...), c.want) })
	}
}

// Each VRF's subnets render a server of their own, kea-dhcp4-<vrf>.json with its control socket
// and lease file, apart from the default VRF's kea-dhcp4.json, so one range serves in two VRFs.
// A server answers on the links its subnets name, and a subnet is Kea's number of its first
// address.
func TestKeaPerVRF(t *testing.T) {
	files, err := unitFiles(t, []string{"kea"}, keaDocs(t,
		subnetDoc(t, "office", `{"interfaces": ["lan1", "lan0"], "subnet": "192.0.2.0/24", "pools": [{"first": "192.0.2.100", "last": "192.0.2.199"}]}`),
		subnetDoc(t, "guests", `{"interfaces": ["vlan20"], "subnet": "192.0.2.0/24", "leaseTime": "1h"}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	if names := slicesSorted(files); !slices.Equal(names, []string{"kea-dhcp4-guest.json", "kea-dhcp4.json"}) {
		t.Fatalf("files = %v", names)
	}
	type server struct {
		Dhcp4 struct {
			Interfaces struct {
				Interfaces []string `json:"interfaces"`
			} `json:"interfaces-config"`
			Sockets []struct {
				Name string `json:"socket-name"`
			} `json:"control-sockets"`
			Leases struct {
				Name string `json:"name"`
			} `json:"lease-database"`
			Subnets []struct {
				ID       uint32 `json:"id"`
				Subnet   string `json:"subnet"`
				Lifetime uint64 `json:"valid-lifetime"`
			} `json:"subnet4"`
		} `json:"Dhcp4"`
	}
	for file, want := range map[string]struct {
		links          []string
		socket, leases string
		lifetime       uint64
	}{
		"kea-dhcp4.json":       {[]string{"lan0", "lan1"}, "/run/kea/dhcp4.sock", "/var/lib/kea/dhcp4.leases", 43200},
		"kea-dhcp4-guest.json": {[]string{"vlan20"}, "/run/kea/dhcp4-guest.sock", "/var/lib/kea/dhcp4-guest.leases", 3600},
	} {
		var s server
		if err := json.Unmarshal([]byte(files[file]), &s); err != nil {
			t.Fatal(err)
		}
		d := s.Dhcp4
		if !slices.Equal(d.Interfaces.Interfaces, want.links) || len(d.Sockets) != 1 || d.Sockets[0].Name != want.socket || d.Leases.Name != want.leases ||
			len(d.Subnets) != 1 || d.Subnets[0].ID != 3221225984 || d.Subnets[0].Subnet != "192.0.2.0/24" || d.Subnets[0].Lifetime != want.lifetime {
			t.Errorf("%s = %s", file, files[file])
		}
	}
}

// slicesSorted are the names of the files, sorted.
func slicesSorted(files map[string]string) []string {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// A subnet's boot files become client classes that the subnet evaluates once Kea chose it: iPXE
// first, then UEFI and BIOS, which leave iPXE's clients alone only when an iPXE file exists, each
// with the boot server as next server when one is given.
func TestKeaBootClasses(t *testing.T) {
	files, err := unitFiles(t, []string{"kea"}, keaDocs(t,
		subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": {"server": null, "bios": "undionly.kpxe", "uefi": "ipxe.efi", "ipxe": null}}`),
	))
	if err != nil {
		t.Fatal(err)
	}
	config := files["kea-dhcp4.json"]
	for _, want := range []string{
		`"evaluate-additional-classes": [
          "office-uefi",
          "office-bios"
        ]`,
		`"test": "(option[93].hex == 0x0007 or option[93].hex == 0x0009)"`,
		`"test": "option[93].hex == 0x0000"`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("kea-dhcp4.json lacks %s:\n%s", want, config)
		}
	}
	if strings.Contains(config, "next-server") || strings.Contains(config, "office-ipxe") {
		t.Errorf("kea-dhcp4.json has a next server or an iPXE class:\n%s", config)
	}
}
