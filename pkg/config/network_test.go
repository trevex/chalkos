package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/networkdschema"
	"github.com/trevex/chalkos/pkg/schema"
)

func networkSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadSchemas("../../test/fixtures/config/networkd/schemas")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func networkDocs(t *testing.T, yaml string) []Document {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestRenderNetworkUnitMatchesNixOS(t *testing.T) {
	docs := networkDocs(t, `
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {name: enp1s0}
  network: {address: [10.0.0.11/24], gateway: 10.0.0.1}
`)
	units, err := renderNetworkUnits(docs, networkSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "[Match]\nName=enp1s0\n\n[Network]\nAddress=10.0.0.11/24\nGateway=10.0.0.1\n\n"
	if units["10-uplink.network"] != want {
		t.Errorf("unit =\n%q\nwant\n%q", units["10-uplink.network"], want)
	}
}

func TestRenderRepeatedSectionsAndValues(t *testing.T) {
	docs := networkDocs(t, `
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-lan}
spec:
  network: {dhcp: "no", ipv6AcceptRA: false, dns: [1.1.1.1, 9.9.9.9]}
  route:
    - {destination: 10.1.0.0/16, gateway: 10.0.0.254}
    - {destination: 10.2.0.0/16, gateway: 10.0.0.253}
`)
	units, err := renderNetworkUnits(docs, networkSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "[Network]\nDHCP=no\nDNS=1.1.1.1\nDNS=9.9.9.9\nIPv6AcceptRA=false\n\n" +
		"[Route]\nDestination=10.1.0.0/16\nGateway=10.0.0.254\n\n" +
		"[Route]\nDestination=10.2.0.0/16\nGateway=10.0.0.253\n\n"
	if units["20-lan.network"] != want {
		t.Errorf("unit =\n%q\nwant\n%q", units["20-lan.network"], want)
	}
}

func TestRenderRejectsNewlines(t *testing.T) {
	docs := networkDocs(t, "apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: a}\nspec:\n  match: {name: \"a\\nb\"}\n")
	if _, err := renderNetworkUnits(docs, networkSchemas(t)); err == nil {
		t.Fatal("rendered a value with a newline")
	}
}

// A networkd value that spans lines would end its key and start another in the unit, so the
// schema refuses it, and chalkctl with it, before the node renders anything.
func TestNetworkValueSpansNoLines(t *testing.T) {
	docs := networkDocs(t, minimal+"---\napiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: a}\nspec:\n  match: {name: \"a\\nb\"}\n")
	if _, err := NewSet(docs, allSchemas(t)); err == nil || !strings.Contains(err.Error(), "/spec/match") {
		t.Fatalf("err = %v, want the value refused", err)
	}
}

func TestRenderUnannotatedSectionFails(t *testing.T) {
	// A hand-built document bypasses the schema's additionalProperties: false, which otherwise
	// rejects a spec property the schema never annotated with x-chalkos-networkd-section.
	docs := []Document{{APIVersion: NetworkdAPI, Kind: "Network", Name: "a", Object: map[string]any{
		"apiVersion": NetworkdAPI, "kind": "Network", "metadata": map[string]any{"name": "a"},
		"spec": map[string]any{"bogus": map[string]any{"foo": "bar"}},
	}}}
	if _, err := renderNetworkUnits(docs, networkSchemas(t)); err == nil {
		t.Fatal("rendered a section with no x-chalkos-networkd-section annotation")
	}
}

func TestRenderUnannotatedKeyFails(t *testing.T) {
	// Likewise for a key the schema never annotated with x-chalkos-networkd-key, inside a
	// section that does exist.
	docs := []Document{{APIVersion: NetworkdAPI, Kind: "Network", Name: "a", Object: map[string]any{
		"apiVersion": NetworkdAPI, "kind": "Network", "metadata": map[string]any{"name": "a"},
		"spec": map[string]any{"match": map[string]any{"bogus": "x"}},
	}}}
	if _, err := renderNetworkUnits(docs, networkSchemas(t)); err == nil {
		t.Fatal("rendered a key with no x-chalkos-networkd-key annotation")
	}
}

func TestNetworkMapHoldsAddressesAndMAC(t *testing.T) {
	docs := networkDocs(t, `
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-a}
spec:
  match: {macAddress: "52:54:00:7b:00:11"}
  network: {address: 10.0.0.11/24}
  address: [{address: "fd00::11/64"}]
`)
	m := NetworkMap(docs)
	nets := m["networks"].(map[string]any)["10-a"].(map[string]any)
	// Only [Network] Address= counts as a static address.
	if got := nets["address"].([]any); len(got) != 1 || got[0] != "10.0.0.11/24" {
		t.Errorf("address = %v", got)
	}
	if mac := nets["matchConfig"].(map[string]any)["MACAddress"]; mac != "52:54:00:7b:00:11" {
		t.Errorf("MACAddress = %v", mac)
	}
}

func TestRenderBoolsAsNixOSDoes(t *testing.T) {
	docs := networkDocs(t, `
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: a}
spec:
  network: {ipv6AcceptRA: true}
`)
	units, err := renderNetworkUnits(docs, networkSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[Network]\nIPv6AcceptRA=true\n\n"; units["a.network"] != want {
		t.Errorf("unit = %q, want %q", units["a.network"], want)
	}
}

func TestNetworkMapWithoutAddressIsEmpty(t *testing.T) {
	docs := networkDocs(t, `
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-dhcp}
spec:
  match: {name: enp1s0}
  network: {dhcp: "yes"}
`)
	nets := NetworkMap(docs)["networks"].(map[string]any)["10-dhcp"].(map[string]any)
	if got, ok := nets["address"].([]any); !ok || got == nil || len(got) != 0 {
		t.Errorf("address = %#v, want []", nets["address"])
	}
	if _, ok := nets["matchConfig"]; ok {
		t.Errorf("matchConfig = %v, want none", nets["matchConfig"])
	}
}

func TestNetworkMapJoinsAListOfMACs(t *testing.T) {
	docs := []Document{{APIVersion: NetworkdAPI, Kind: "Network", Name: "a", Object: map[string]any{
		"spec": map[string]any{"match": map[string]any{"macAddress": []any{"52:54:00:00:00:01", "52:54:00:00:00:02"}}},
	}}}
	nets := NetworkMap(docs)["networks"].(map[string]any)["a"].(map[string]any)
	if mac := nets["matchConfig"].(map[string]any)["MACAddress"]; mac != "52:54:00:00:00:01 52:54:00:00:00:02" {
		t.Errorf("MACAddress = %#v", mac)
	}
}

func TestStaticAddresses(t *testing.T) {
	docs := mustParse(t, `apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-b}
spec: {network: {address: [10.0.1.5/24]}}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-a}
spec: {network: {address: [10.0.0.11/24, fd00::11/64]}}
`)
	if got, want := StaticAddresses(docs), []string{"10.0.0.11", "fd00::11", "10.0.1.5"}; !slices.Equal(got, want) {
		t.Errorf("addresses = %v, want %v", got, want)
	}
}

// networkdSchema is the schema of a networkd kind whose spec holds the sections given, each with
// its keys by YAML name, annotated as the image's schemas annotate them.
func networkdSchema(kind string, sections map[string]map[string]string) map[string]any {
	specProps := map[string]any{}
	defs := map[string]any{}
	for section, keys := range sections {
		props := map[string]any{}
		for yamlKey, key := range keys {
			props[yamlKey] = map[string]any{"x-chalkos-networkd-key": key}
		}
		defs[section] = map[string]any{"type": "object", "properties": props, "x-chalkos-networkd-section": section}
		specProps[networkdschema.CamelCase(section)] = map[string]any{"$ref": "#/$defs/" + section, "x-chalkos-networkd-section": section}
	}
	return map[string]any{
		"$schema":        "https://json-schema.org/draft/2020-12/schema",
		"title":          kind,
		"type":           "object",
		"x-chalkos-kind": map[string]any{"group": "networkd.chalkos.dev", "version": "v1alpha1", "kind": kind, "multiple": true},
		"$defs":          defs,
		"properties": map[string]any{
			"apiVersion": map[string]any{"const": "networkd.chalkos.dev/v1alpha1"},
			"kind":       map[string]any{"const": kind},
			"metadata":   map[string]any{"type": "object"},
			"spec":       map[string]any{"type": "object", "properties": specProps},
		},
	}
}

// TestRenderNetDevAndLinkUnits renders NetDev and Link documents as .netdev and .link units, as
// NixOS writes systemd.network.netdevs and links.
func TestRenderNetDevAndLinkUnits(t *testing.T) {
	dir := t.TempDir()
	for kind, sections := range map[string]map[string]map[string]string{
		"NetDev": {"NetDev": {"kind": "Kind", "name": "Name"}, "VLAN": {"id": "Id"}},
		"Link":   {"Match": {"macAddress": "MACAddress"}, "Link": {"name": "Name"}},
	} {
		data, err := json.Marshal(networkdSchema(kind, sections))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "networkd.chalkos.dev_v1alpha1_"+kind+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := schema.LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := networkDocs(t, `
apiVersion: networkd.chalkos.dev/v1alpha1
kind: NetDev
metadata: {name: 30-vlan}
spec:
  netDev: {name: vlan10, kind: vlan}
  vlan: {id: 10}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Link
metadata: {name: 40-nic}
spec:
  match: {macAddress: "aa:bb:cc:dd:ee:01"}
  link: {name: uplink}
`)
	units, err := renderNetworkUnits(docs, s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"30-vlan.netdev": "[NetDev]\nKind=vlan\nName=vlan10\n\n[VLAN]\nId=10\n\n",
		"40-nic.link":    "[Match]\nMACAddress=aa:bb:cc:dd:ee:01\n\n[Link]\nName=uplink\n\n",
	}
	if !reflect.DeepEqual(units, want) {
		t.Errorf("units = %q, want %q", units, want)
	}
}
