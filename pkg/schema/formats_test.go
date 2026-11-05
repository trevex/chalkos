package schema

import (
	"strings"
	"testing"
)

// PortRange reads a port or a range of ports from 1 to 65535 that runs forwards, in decimal
// without a sign or leading zeros.
func TestPortRange(t *testing.T) {
	for s, want := range map[string][2]int{
		"22":        {22, 22},
		"1-65535":   {1, 65535},
		"8000-8100": {8000, 8100},
		"443-443":   {443, 443},
	} {
		first, last, ok := PortRange(s)
		if !ok || first != want[0] || last != want[1] {
			t.Errorf("PortRange(%q) = %d, %d, %v; want %d, %d", s, first, last, ok, want[0], want[1])
		}
	}
	for _, s := range []string{"", "0", "65536", "9-1", "0-10", "1-65536", "70000-80000", "01-10", "+1-2", "1-", "-1", "1--2", "1-2-3", "a-b", " 1-2"} {
		if _, _, ok := PortRange(s); ok {
			t.Errorf("PortRange(%q) is ok", s)
		}
	}
}

// The formats a kind's schema asserts refuse what ip(), cidr() and portRange() do not read, and
// say what they take. A value of another type passes them, for "type" to refuse.
func TestFormats(t *testing.T) {
	doc := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "Hop", "type": "object",
		"properties": {
			"apiVersion": {"const": "r.example.org/v1"}, "kind": {"const": "Hop"},
			"metadata": {"type": "object"},
			"spec": {"type": "object", "properties": {
				"address": {"type": "string", "format": "ip"},
				"prefixes": {"type": "array", "items": {"type": "string", "format": "cidr"}},
				"ports": {"anyOf": [{"type": "integer"}, {"type": "string", "format": "port-range"}]}
			}}
		},
		"x-chalkos-kind": {"group": "r.example.org", "version": "v1", "kind": "Hop", "multiple": true}
	}`
	s, err := LoadFiles(map[string][]byte{"r.example.org_v1_Hop.json": []byte(doc)})
	if err != nil {
		t.Fatal(err)
	}
	for spec, want := range map[string]string{
		`{"address": "192.0.2.1"}`:                         "",
		`{"address": "2001:DB8::1"}`:                       "",
		`{"address": "::ffff:192.0.2.1"}`:                  "",
		`{"address": "192.0.2.256"}`:                       `at '/spec/address': '192.0.2.256' is not valid ip: an address is an IPv4 address of four numbers from 0 to 255 without leading zeros, or an IPv6 address without a zone`,
		`{"address": "192.0.2.01"}`:                        "is not valid ip",
		`{"address": "fe80::1%eth0"}`:                      "is not valid ip",
		`{"address": "192.0.2.0/24"}`:                      "is not valid ip",
		`{"prefixes": ["192.0.2.0/24", "2001:db8::1/64"]}`: "",
		`{"prefixes": ["192.0.2.0/24", "192.0.2.0/33"]}`:   `at '/spec/prefixes/1': '192.0.2.0/33' is not valid cidr: an address range is an address, a slash and a prefix length of at most 32 for IPv4 or 128 for IPv6`,
		`{"prefixes": ["192.0.2.1"]}`:                      "is not valid cidr",
		`{"ports": 22}`:                                    "",
		`{"ports": "8000-8100"}`:                           "",
		`{"ports": "9-1"}`:                                 `at '/spec/ports': '9-1' is not valid port-range: a range of ports is "<first>-<last>", each from 1 to 65535, and the first no higher than the last`,
		`{"ports": "70000-80000"}`:                         "is not valid port-range",
		`{"ports": "22"}`:                                  `'22' is not valid port-range: a range of ports is "<first>-<last>", each from 1 to 65535, and the first no higher than the last; a single port is a number, unquoted`,
		`{"address": 7}`:                                   "got number, want string",
	} {
		err := s.Validate(decode(t, `{"apiVersion": "r.example.org/v1", "kind": "Hop", "metadata": {"name": "a"}, "spec": `+spec+`}`))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", spec, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err = %v, want one with %q", spec, err, want)
		}
	}
}
