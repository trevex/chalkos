package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// keaDocs are a LAN router's documents with the DHCPSubnet documents given: lan0 holds
// 192.0.2.1/24 and 192.0.2.30/24, lan1 198.51.100.1/24 and 192.0.2.65/26, vlan20 in the VRF guest
// 192.0.2.1/24 as well and lan2, in the VRF ghost of no VRF document, 203.0.113.1/24; the NAT64
// edge has the link n64-edge.
func keaDocs(t *testing.T, subnets ...any) map[string][]any {
	t.Helper()
	return map[string][]any{
		"VRF":   {routerDoc(t, "VRF", "guest", `{"table": 300, "interfaces": ["vlan20"]}`)},
		"NAT64": {routerDoc(t, "NAT64", "edge", `{"vrf": null, "prefix": "64:ff9b::/96", "pool4": []}`)},
		"Network": {
			document(t, "Network", "10-lan0", `{"match": {"name": "lan0"}, "network": {"address": ["192.0.2.1/24", "192.0.2.30/24"]}}`),
			document(t, "Network", "11-lan1", `{"match": {"name": "lan1"}, "network": {"address": ["198.51.100.1/24", "192.0.2.65/26"]}}`),
			document(t, "Network", "12-lan2", `{"match": {"name": "lan2"}, "network": {"vrf": ["ghost"], "address": ["203.0.113.1/24"]}}`),
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
	full := map[string]any{"pools": []any{}, "gateway": nil, "dns": []any{}, "domain": nil, "leaseTime": "12h", "reservations": []any{}, "boot": nil, "prefixDelegation": nil}
	for k, v := range s {
		full[k] = v
	}
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	return routerDoc(t, "DHCPSubnet", name, string(data))
}

// The renderer refuses what spans documents: a subnet's link that is a VRF's device or a NAT64's
// link, a link of no Network, links of two VRFs or of a VRF no document declares, a link without
// the router's address in the subnet, a link of two subnets, subnets that overlap in a VRF, and a
// pool or reservation of the router's own address. It guards, with an internal error, values
// that Kea's check passes and that would panic the renderer or that Kea's option data would read
// as more values, and names that dnsmasq's config and the firewall's ruleset would read as
// syntax. The rules refuse the rest of what a subnet says wrong, and Kea's check what it cannot
// load.
func TestKeaRefusals(t *testing.T) {
	office := func(more string) any {
		return subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"`+more+`}`)
	}
	pool := func(first, last string) any {
		return office(`, "pools": [{"first": "` + first + `", "last": "` + last + `"}]`)
	}
	reserve := func(address string) any {
		return office(`, "reservations": [{"mac": "52:54:00:12:34:56", "address": "` + address + `", "hostname": null}]`)
	}
	const checked = "; chalkd checks the config against the schemas, the rules and the validators before it renders"
	for name, c := range map[string]struct {
		subnets []any
		want    string
	}{
		"a VRF's device":         {[]any{subnetDoc(t, "office", `{"interfaces": ["guest"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[0]: guest is the device of VRF guest; name the VRF's links"},
		"a NAT64's link":         {[]any{subnetDoc(t, "office", `{"interfaces": ["n64-edge"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet/office: spec.interfaces[0]: n64-edge is the link of NAT64 edge"},
		"links of two VRFs":      {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0", "vlan20"], "subnet": "192.0.2.0/24"}`)}, "its links are in VRF default and VRF guest; a subnet is served in one VRF"},
		"a VRF of no document":   {[]any{subnetDoc(t, "office", `{"interfaces": ["lan2"], "subnet": "203.0.113.0/24"}`)}, `DHCPSubnet/office: spec.interfaces: its links are in the VRF "ghost", which no VRF document declares`},
		"a link of no Network":   {[]any{subnetDoc(t, "office", `{"interfaces": ["lan9"], "subnet": "192.0.2.0/24"}`)}, "no Network document matches lan9 by Name= alone"},
		"a link without address": {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "203.0.113.0/24"}`)}, "Network 10-lan0 gives lan0 no static address in the subnet 203.0.113.0/24, which Kea answers its clients from"},
		"a link of two subnets":  {[]any{office(""), subnetDoc(t, "other", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"}`)}, "DHCPSubnet office and DHCPSubnet other both serve lan0; a link takes one subnet"},
		"overlapping subnets":    {[]any{office(""), subnetDoc(t, "servers", `{"interfaces": ["lan1"], "subnet": "192.0.2.64/26"}`)}, "DHCPSubnet office and DHCPSubnet servers overlap in VRF default: 192.0.2.0/24 and 192.0.2.64/26"},
		"a pool's first address": {[]any{pool("192.0.2.1", "192.0.2.10")}, "DHCPSubnet/office: spec.pools[0]: the pool 192.0.2.1-192.0.2.10 holds 192.0.2.1, the router's own address on the subnet's links"},
		"a pool of the router's": {[]any{pool("192.0.2.10", "192.0.2.50")}, "DHCPSubnet/office: spec.pools[0]: the pool 192.0.2.10-192.0.2.50 holds 192.0.2.30, the router's own address on the subnet's links"},
		"the router reserved":    {[]any{reserve("192.0.2.30")}, "DHCPSubnet/office: spec.reservations[0].address: the reservation of 52:54:00:12:34:56, 192.0.2.30, is the router's own address on the subnet's links"},
		// Values that Kea's check passes and the renderer would panic on, or that Kea's option
		// data would read as more values.
		"no interface":                {[]any{subnetDoc(t, "office", `{"interfaces": [], "subnet": "192.0.2.0/24"}`)}, "internal error: DHCPSubnet office: interfaces names no interface" + checked},
		"a glob":                      {[]any{subnetDoc(t, "office", `{"interfaces": ["lan*"], "subnet": "192.0.2.0/24"}`)}, `internal error: DHCPSubnet office: interfaces: "lan*" is no interface name` + checked},
		"an interface of a quote":     {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0\""], "subnet": "192.0.2.0/24"}`)}, `internal error: DHCPSubnet office: interfaces: "lan0\"" is no interface name`},
		"a subnet of no range":        {[]any{subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0"}`)}, `internal error: DHCPSubnet office: the subnet "192.0.2.0" is no address range` + checked},
		"a pool of no address":        {[]any{pool("192.0.2.100", "192.0.2.x")}, `internal error: DHCPSubnet office: "192.0.2.x" is no address without a zone` + checked},
		"a reservation of no address": {[]any{reserve("printer")}, `internal error: DHCPSubnet office: "printer" is no address without a zone`},
		"a gateway of no address":     {[]any{office(`, "gateway": "router"`)}, `internal error: DHCPSubnet office: "router" is no address without a zone`},
		"a boot server of no address": {[]any{office(`, "boot": {"server": "tftp", "bios": "undionly.kpxe", "uefi": null, "ipxe": null}`)}, `internal error: DHCPSubnet office: "tftp" is no address without a zone`},
		"a DNS server of a zone":      {[]any{office(`, "dns": ["fe80::53%lan0,192.0.2.66"]`)}, `internal error: DHCPSubnet office: "fe80::53%lan0,192.0.2.66" is no address without a zone`},
		"a domain of a comma":         {[]any{office(`, "domain": "lan.example.org,evil.example.org"`)}, `internal error: DHCPSubnet office: "lan.example.org,evil.example.org" is no domain` + checked},
		"a lease with a space":        {[]any{office(`, "leaseTime": "12 h"`)}, `internal error: DHCPSubnet office: leaseTime "12 h" is no number and s, m, h or d` + checked},
	} {
		t.Run(name, func(t *testing.T) { refused(t, []string{"kea", testPDHook}, keaDocs(t, c.subnets...), c.want) })
	}
}

// Each VRF's subnets render a server of their own, kea-dhcp4-<vrf>.json with its control socket
// and lease file, apart from the default VRF's kea-dhcp4.json, so one range serves in two VRFs.
// A server answers on the links its subnets name, and a subnet is Kea's number of its first
// address.
func TestKeaPerVRF(t *testing.T) {
	files, err := unitFiles(t, []string{"kea", testPDHook}, keaDocs(t,
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
// first, then UEFI and BIOS, which leave iPXE's clients alone only when an iPXE file exists. BIOS
// and UEFI files take the boot server as next server; an iPXE script of an HTTP URL needs none.
func TestKeaBootClasses(t *testing.T) {
	render := func(boot string) string {
		t.Helper()
		files, err := unitFiles(t, []string{"kea", testPDHook}, keaDocs(t,
			subnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": `+boot+`}`),
		))
		if err != nil {
			t.Fatal(err)
		}
		return files["kea-dhcp4.json"]
	}
	config := render(`{"server": "192.0.2.5", "bios": "undionly.kpxe", "uefi": "ipxe.efi", "ipxe": null}`)
	for _, want := range []string{
		`"evaluate-additional-classes": [
          "office-uefi",
          "office-bios"
        ]`,
		`"test": "(option[93].hex == 0x0007 or option[93].hex == 0x0009)"`,
		`"test": "option[93].hex == 0x0000"`,
		`"next-server": "192.0.2.5"`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("kea-dhcp4.json lacks %s:\n%s", want, config)
		}
	}
	if strings.Contains(config, "office-ipxe") {
		t.Errorf("kea-dhcp4.json has an iPXE class:\n%s", config)
	}
	config = render(`{"server": null, "bios": null, "uefi": null, "ipxe": "http://192.0.2.5/boot.ipxe"}`)
	if !strings.Contains(config, `"test": "substring(option[77].hex, 0, 4) == 'iPXE'"`) || strings.Contains(config, "next-server") {
		t.Errorf("kea-dhcp4.json lacks the iPXE class or has a next server:\n%s", config)
	}
}
