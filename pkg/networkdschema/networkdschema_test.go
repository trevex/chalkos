package networkdschema

import (
	"reflect"
	"strings"
	"testing"
)

func TestCamelCase(t *testing.T) {
	for name, want := range map[string]string{
		"Name":                     "name",
		"Network":                  "network",
		"DHCP":                     "dhcp",
		"LLDP":                     "lldp",
		"L2TP":                     "l2tp",
		"MACAddress":               "macAddress",
		"MTUBytes":                 "mtuBytes",
		"IPv6AcceptRA":             "ipv6AcceptRA",
		"IPv4LL":                   "ipv4LL",
		"DHCPv6PrefixDelegation":   "dhcpv6PrefixDelegation",
		"IPoIB":                    "ipoib",
		"MACsec":                   "macsec",
		"MACsecReceiveAssociation": "macsecReceiveAssociation",
		"QDisc":                    "qDisc",
		"L2TPSession":              "l2tpSession",
		"L2MissNotification":       "l2MissNotification",
		"TCP6SegmentationOffload":  "tcp6SegmentationOffload",
		"UseDNS":                   "useDNS",
		"SR-IOV":                   "sriov",
		"VLANId":                   "vlanId",
		"IPDoNotFragment":          "ipDoNotFragment",
		"SR-IOVVirtualFunctions":   "sriovVirtualFunctions",
	} {
		if got := CamelCase(name); got != want {
			t.Errorf("CamelCase(%q) = %q, want %q", name, got, want)
		}
	}
}

const table = `/* SPDX-License-Identifier: LGPL-2.1-or-later */
%{
#include "x.h"
%}
struct ConfigPerfItem;
%%
Match.Name,                 config_parse_match_ifnames, 1, offsetof(Network, match.ifname)
/* a comment */
Network.Address,            config_parse_address,       0, 0
Network.IPv6AcceptRA,       config_parse_tristate,      0, 0
Network.Address,            config_parse_address,       0, 0
SR-IOV.VLANId,              config_parse_sr_iov_uint32, 0, 0
`

func TestParse(t *testing.T) {
	entries, err := Parse(strings.NewReader(table))
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"Match", "Name"},
		{"Network", "Address"},
		{"Network", "IPv6AcceptRA"},
		{"Network", "Address"},
		{"SR-IOV", "VLANId"},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entries[%d] = %v, want %v", i, entries[i], want[i])
		}
	}
}

func TestParseWithoutEntriesFails(t *testing.T) {
	if _, err := Parse(strings.NewReader("%%\n")); err == nil {
		t.Fatal("parsed a table without entries")
	}
}

func TestParseRejectsMalformedLine(t *testing.T) {
	bad := "%%\nNetwork.Address, config_parse_address, 0, 0\nthis is not an entry\n"
	_, err := Parse(strings.NewReader(bad))
	if err == nil {
		t.Fatal("parsed a table with a malformed line")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("err = %v, want it to name line 3", err)
	}
}

func TestSpecSchema(t *testing.T) {
	entries, err := Parse(strings.NewReader(table))
	if err != nil {
		t.Fatal(err)
	}
	s, err := SpecSchema(entries)
	if err != nil {
		t.Fatal(err)
	}
	spec := s["spec"].(map[string]any)
	if spec["additionalProperties"] != false {
		t.Error("spec accepts unknown sections")
	}
	network := spec["properties"].(map[string]any)["network"].(map[string]any)
	if network["x-chalkos-networkd-section"] != "Network" {
		t.Errorf("network's spec property x-chalkos-networkd-section = %v, want Network", network["x-chalkos-networkd-section"])
	}
	networkAnyOf := network["anyOf"].([]any)
	ref := networkAnyOf[0].(map[string]any)["$ref"]
	if ref != "#/$defs/Network" {
		t.Errorf("network refers to %v", ref)
	}
	wantManyNetwork := map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/Network"}}
	if !reflect.DeepEqual(networkAnyOf[1], wantManyNetwork) {
		t.Errorf("network's array branch = %v, want %v", networkAnyOf[1], wantManyNetwork)
	}
	if _, ok := spec["properties"].(map[string]any)["sriov"]; !ok {
		t.Error("section SR-IOV is missing as sriov")
	}
	def := s["$defs"].(map[string]any)["Network"].(map[string]any)
	if def["x-chalkos-networkd-section"] != "Network" || def["additionalProperties"] != false {
		t.Errorf("Network definition = %v", def)
	}
	props := def["properties"].(map[string]any)
	if len(props) != 2 {
		t.Errorf("Network has %d keys, want 2: %v", len(props), props)
	}
	ra := props["ipv6AcceptRA"].(map[string]any)
	if ra["x-chalkos-networkd-key"] != "IPv6AcceptRA" {
		t.Errorf("ipv6AcceptRA = %v", ra)
	}
	raAnyOf := ra["anyOf"].([]any)
	wantScalar := map[string]any{"type": []any{"string", "integer", "boolean"}}
	if !reflect.DeepEqual(raAnyOf[0], wantScalar) {
		t.Errorf("ipv6AcceptRA's scalar branch = %v, want %v", raAnyOf[0], wantScalar)
	}
	wantManyScalar := map[string]any{"type": "array", "items": wantScalar}
	if !reflect.DeepEqual(raAnyOf[1], wantManyScalar) {
		t.Errorf("ipv6AcceptRA's array branch = %v, want %v", raAnyOf[1], wantManyScalar)
	}
}

func TestSpecSchemaRejectsCollisions(t *testing.T) {
	_, err := SpecSchema([]Entry{{"Network", "DNS"}, {"Network", "Dns"}})
	if err == nil || !strings.Contains(err.Error(), "dns") {
		t.Fatalf("err = %v, want a collision on dns", err)
	}
}
