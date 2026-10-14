package networkdschema

import (
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
		"IPoIB":                    "ipoIB",
		"MACsec":                   "macsec",
		"MACsecReceiveAssociation": "macsecReceiveAssociation",
		"QDisc":                    "qDisc",
		"L2TPSession":              "l2tpSession",
		"L2MissNotification":       "l2MissNotification",
		"TCP6SegmentationOffload":  "tcp6SegmentationOffload",
		"UseDNS":                   "useDNS",
		"SR-IOV":                   "srIOV",
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
	ref := network["anyOf"].([]any)[0].(map[string]any)["$ref"]
	if ref != "#/$defs/Network" {
		t.Errorf("network refers to %v", ref)
	}
	if _, ok := spec["properties"].(map[string]any)["srIOV"]; !ok {
		t.Error("section SR-IOV is missing as srIOV")
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
}

func TestSpecSchemaRejectsCollisions(t *testing.T) {
	_, err := SpecSchema([]Entry{{"Network", "DNS"}, {"Network", "Dns"}})
	if err == nil || !strings.Contains(err.Error(), "dns") {
		t.Fatalf("err = %v, want a collision on dns", err)
	}
}
