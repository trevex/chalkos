package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// networkFixture holds the schema of networkd's Network kind, as an image generates it, with some
// of its sections.
const networkFixture = "../../test/fixtures/config/networkd/schemas"

// networkd renders the documents of input, by kind, with the schemas of dir and returns the unit
// files by name.
func networkd(t *testing.T, dir string, documents map[string][]any) (map[string]string, error) {
	t.Helper()
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "cp1"}, "documents": documents})
	if err != nil {
		t.Fatal(err)
	}
	out, err := render(t, []string{"networkd", dir}, string(input))
	if err != nil {
		return nil, err
	}
	units := map[string]string{}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		units[e.Name()] = string(data)
	}
	return units, nil
}

// document is a networkd document of kind called name, with spec in JSON.
func document(t *testing.T, kind, name, spec string) any {
	t.Helper()
	var s any
	dec := json.NewDecoder(strings.NewReader(spec))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"apiVersion": "networkd.chalkos.dev/v1alpha1", "kind": kind, "metadata": map[string]any{"name": name}, "spec": s}
}

func TestNetworkdUnitMatchesNixOS(t *testing.T) {
	units, err := networkd(t, networkFixture, map[string][]any{"Network": {
		document(t, "Network", "10-uplink", `{"match": {"name": "enp1s0"}, "network": {"address": ["10.0.0.11/24"], "gateway": "10.0.0.1"}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"10-uplink.network": "[Match]\nName=enp1s0\n\n[Network]\nAddress=10.0.0.11/24\nGateway=10.0.0.1\n\n"}
	if !reflect.DeepEqual(units, want) {
		t.Errorf("units = %q, want %q", units, want)
	}
}

func TestNetworkdRepeatedSectionsAndValues(t *testing.T) {
	units, err := networkd(t, networkFixture, map[string][]any{"Network": {
		document(t, "Network", "20-lan", `{"network": {"dhcp": "no", "ipv6AcceptRA": false, "dns": ["1.1.1.1", "9.9.9.9"]},
		  "route": [{"destination": "10.1.0.0/16", "gateway": "10.0.0.254"}, {"destination": "10.2.0.0/16", "gateway": "10.0.0.253"}]}`),
	}})
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

func TestNetworkdBoolsAsNixOSRendersThem(t *testing.T) {
	units, err := networkd(t, networkFixture, map[string][]any{"Network": {
		document(t, "Network", "a", `{"network": {"ipv6AcceptRA": true}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "[Network]\nIPv6AcceptRA=true\n\n"; units["a.network"] != want {
		t.Errorf("unit = %q, want %q", units["a.network"], want)
	}
}

// Without documents the output holds no unit, so the image's own units apply.
func TestNetworkdWithoutDocuments(t *testing.T) {
	units, err := networkd(t, filepath.Join(t.TempDir(), "none"), map[string][]any{"Link": {}, "NetDev": {}, "Network": {}})
	if err != nil || len(units) != 0 {
		t.Errorf("units = %v, %v; want none", units, err)
	}
}

// A document that bypassed validation renders no value that spans lines, no section or key the
// schema does not annotate, and no kind whose schema the image lacks.
func TestNetworkdRefusesWhatTheSchemasRefuse(t *testing.T) {
	for name, c := range map[string]struct {
		kind, spec, want string
	}{
		"a value that spans lines": {"Network", `{"match": {"name": "a\nb"}}`, "Network a: Match.Name: a value cannot span lines"},
		"an object as a value":     {"Network", `{"match": {"name": {"x": 1}}}`, "is not a string, number or boolean"},
		"an unannotated section":   {"Network", `{"bogus": {"foo": "bar"}}`, "spec.bogus has no x-chalkos-networkd-section annotation"},
		"an unannotated key":       {"Network", `{"match": {"bogus": "x"}}`, "Match.bogus has no x-chalkos-networkd-key annotation"},
		"a kind without a schema":  {"NetDev", `{"netDev": {"name": "x"}}`, "the image's networkd schema"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := networkd(t, networkFixture, map[string][]any{c.kind: {document(t, c.kind, "a", c.spec)}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// networkdSchema is the schema of a networkd kind whose spec holds the sections given, each by
// its YAML name with its keys by YAML name, annotated as the image's schemas annotate them.
func networkdSchema(kind string, sections map[string]map[string]string) map[string]any {
	specProps := map[string]any{}
	defs := map[string]any{}
	for section, keys := range sections {
		name, keys := keys["."], keys
		props := map[string]any{}
		for yamlKey, key := range keys {
			if yamlKey != "." {
				props[yamlKey] = map[string]any{"x-chalkos-networkd-key": key}
			}
		}
		defs[name] = map[string]any{"type": "object", "properties": props, "x-chalkos-networkd-section": name}
		specProps[section] = map[string]any{"$ref": "#/$defs/" + name, "x-chalkos-networkd-section": name}
	}
	return map[string]any{
		"$defs":      defs,
		"properties": map[string]any{"spec": map[string]any{"type": "object", "properties": specProps}},
	}
}

// NetDev and Link documents render as .netdev and .link units, as NixOS writes
// systemd.network.netdevs and links, with numbers as written.
func TestNetworkdNetDevAndLinkUnits(t *testing.T) {
	dir := t.TempDir()
	for kind, sections := range map[string]map[string]map[string]string{
		"NetDev": {"netDev": {".": "NetDev", "kind": "Kind", "name": "Name"}, "vlan": {".": "VLAN", "id": "Id"}},
		"Link":   {"match": {".": "Match", "macAddress": "MACAddress"}, "link": {".": "Link", "name": "Name"}},
	} {
		data, err := json.Marshal(networkdSchema(kind, sections))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "networkd.chalkos.dev_v1alpha1_"+kind+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	units, err := networkd(t, dir, map[string][]any{
		"NetDev":  {document(t, "NetDev", "30-vlan", `{"netDev": {"name": "vlan10", "kind": "vlan"}, "vlan": {"id": 4094}}`)},
		"Link":    {document(t, "Link", "40-nic", `{"match": {"macAddress": "aa:bb:cc:dd:ee:01"}, "link": {"name": "uplink"}}`)},
		"Network": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"30-vlan.netdev": "[NetDev]\nKind=vlan\nName=vlan10\n\n[VLAN]\nId=4094\n\n",
		"40-nic.link":    "[Match]\nMACAddress=aa:bb:cc:dd:ee:01\n\n[Link]\nName=uplink\n\n",
	}
	if !reflect.DeepEqual(units, want) {
		t.Errorf("units = %q, want %q", units, want)
	}
}

// sectionDef follows the property's $ref, its own or that of a branch of its anyOf, whatever the
// definition is named.
func TestSectionDefFollowsTheRef(t *testing.T) {
	def := map[string]any{"type": "object"}
	defs := map[string]any{"section0": def}
	ref := map[string]any{"$ref": "#/$defs/section0"}
	for name, prop := range map[string]map[string]any{
		"own":       {"$ref": "#/$defs/section0", "x-chalkos-networkd-section": "Network"},
		"one":       {"anyOf": []any{ref}, "x-chalkos-networkd-section": "Network"},
		"many only": {"anyOf": []any{map[string]any{"type": "array", "items": ref}}, "x-chalkos-networkd-section": "Network"},
	} {
		if got := sectionDef(defs, prop); got["type"] != "object" {
			t.Errorf("%s: sectionDef = %v", name, got)
		}
	}
	if got := sectionDef(defs, map[string]any{"x-chalkos-networkd-section": "section0"}); got != nil {
		t.Errorf("sectionDef of a property without $ref = %v", got)
	}
}
