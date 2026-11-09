package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// The renderer guards, with an internal error, values that Kea's check passes and that would
// panic the renderer or that Kea's option data would read as more values. The validator router
// refuses what spans documents, the rules the rest of what a subnet says wrong, and Kea's check
// what it cannot load.
func TestKeaRefusals(t *testing.T) {
	office := func(more string) any {
		return derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24"`+more+`}`)
	}
	const checked = "; chalkd checks the config against the schemas, the rules and the validators before it renders"
	for name, c := range map[string]struct {
		subnets []any
		want    string
	}{
		"a gateway of no address":     {[]any{office(`, "gateway": "router"`)}, `internal error: DHCPSubnet office: ParseAddr("router"): unable to parse IP`},
		"a boot server of no address": {[]any{office(`, "boot": {"server": "tftp", "bios": "undionly.kpxe", "uefi": null, "ipxe": null}`)}, `internal error: DHCPSubnet office: ParseAddr("tftp"): unable to parse IP`},
		"a DNS server of a zone":      {[]any{office(`, "dns": ["fe80::53%lan0,192.0.2.66"]`)}, `internal error: DHCPSubnet office: spec.dns[0]: "fe80::53%lan0,192.0.2.66" has a zone`},
		"a domain of a comma":         {[]any{office(`, "domain": "lan.example.org,evil.example.org"`)}, `internal error: DHCPSubnet office: "lan.example.org,evil.example.org" is no domain` + checked},
		"a lease with a space":        {[]any{office(`, "leaseTime": "12 h"`)}, `internal error: DHCPSubnet office: leaseTime "12 h" is no number and s, m, h or d` + checked},
	} {
		t.Run(name, func(t *testing.T) {
			refused(t, []string{"kea", testPDHook}, derivetest.KeaDocs(t, c.subnets...), c.want)
		})
	}
}

// Each VRF's subnets render a server of their own, kea-dhcp4-<vrf>.json with its control socket
// and lease file, apart from the default VRF's kea-dhcp4.json, so one range serves in two VRFs.
// A server answers on the links its subnets name, and a subnet is Kea's number of its first
// address.
func TestKeaPerVRF(t *testing.T) {
	files, err := unitFiles(t, []string{"kea", testPDHook}, derivetest.KeaDocs(t,
		derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan1", "lan0"], "subnet": "192.0.2.0/24", "pools": [{"first": "192.0.2.100", "last": "192.0.2.199"}]}`),
		derivetest.SubnetDoc(t, "guests", `{"interfaces": ["vlan20"], "subnet": "192.0.2.0/24", "leaseTime": "1h"}`),
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
		files, err := unitFiles(t, []string{"kea", testPDHook}, derivetest.KeaDocs(t,
			derivetest.SubnetDoc(t, "office", `{"interfaces": ["lan0"], "subnet": "192.0.2.0/24", "boot": `+boot+`}`),
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
