package dns

import (
	"strings"
	"testing"
)

// IsName takes lower-case names of 253 characters at most, labels of 63, and refuses what
// Unbound's config would read as syntax.
func TestIsName(t *testing.T) {
	label := strings.Repeat("a", 63)
	long := strings.Repeat(label+".", 4)[:254]
	for s, want := range map[string]bool{
		"example.com":           true,
		"_sip._tcp.example.com": true,
		"_":                     true,
		"a-b.c":                 true,
		label + ".com":          true,
		long[:253]:              true,
		"":                      false,
		"Example.com":           false,
		"example.com.":          false,
		"-a.com":                false,
		"a-.com":                false,
		"a..com":                false,
		"a b.com":               false,
		"a\".com":               false,
		label + "a.com":         false,
		long:                    false,
	} {
		if got := IsName(s); got != want {
			t.Errorf("IsName(%q) = %v, want %v", s, got, want)
		}
	}
}

// IsRecordData takes the data of each record type as Unbound's local-data takes it, and no data of
// another type.
func TestIsRecordData(t *testing.T) {
	for _, c := range []struct {
		t, v string
		want bool
	}{
		{"A", "192.0.2.1", true},
		{"A", "2001:db8::1", false},
		{"A", "192.0.2.01", false},
		{"AAAA", "2001:db8::1", true},
		{"AAAA", "192.0.2.1", false},
		{"AAAA", "::ffff:192.0.2.1", false},
		{"AAAA", "fe80::1%eth0", false},
		{"CNAME", "www.example.com", true},
		{"CNAME", "www.example.com.", false},
		{"PTR", "host.example.com", true},
		{"PTR", "192.0.2.1 x", false},
		{"MX", "10 mail.example.com", true},
		{"MX", "65535 mail.example.com", true},
		{"MX", "65536 mail.example.com", false},
		{"MX", "010 mail.example.com", false},
		{"MX", "+10 mail.example.com", false},
		{"MX", "10  mail.example.com", false},
		{"MX", "mail.example.com", false},
		{"SRV", "0 5 5060 sip.example.com", true},
		{"SRV", "0 5 sip.example.com", false},
		{"SRV", "0 5 70000 sip.example.com", false},
		{"TXT", "v=spf1 -all", true},
		{"TXT", strings.Repeat("x", 255), true},
		{"TXT", "", false},
		{"TXT", strings.Repeat("x", 256), false},
		{"TXT", `say "hi"`, false},
		{"TXT", "it's", false},
		{"TXT", `a\b`, false},
		{"TXT", "a\nb", false},
		{"TXT", "café", false},
		{"NS", "ns.example.com", false},
		{"a", "192.0.2.1", false},
	} {
		if got := IsRecordData(c.t, c.v); got != c.want {
			t.Errorf("IsRecordData(%q, %q) = %v, want %v", c.t, c.v, got, c.want)
		}
	}
}
