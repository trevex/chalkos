package config

import (
	"slices"
	"strings"
	"testing"
)

func networkDocs(t *testing.T, yaml string) []Document {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// A networkd value that spans lines would end its key and start another in the unit, so the
// schema refuses it, and chalkctl with it, before the node renders anything.
func TestNetworkValueSpansNoLines(t *testing.T) {
	docs := networkDocs(t, minimal+"---\napiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: a}\nspec:\n  match: {name: \"a\\nb\"}\n")
	if _, err := NewSet(docs, allSchemas(t)); err == nil || !strings.Contains(err.Error(), "/spec/match") {
		t.Fatalf("err = %v, want the value refused", err)
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
