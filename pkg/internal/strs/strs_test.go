package strs

import (
	"net/netip"
	"testing"
)

func TestJoin(t *testing.T) {
	addrs := []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("fd00::1")}
	if got := Join(addrs, ","); got != "10.0.0.1,fd00::1" {
		t.Errorf("Join = %q", got)
	}
	if got := Join([]netip.Addr(nil), ","); got != "" {
		t.Errorf("Join of none = %q", got)
	}
}

func TestAnd(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b and c"},
	} {
		if got := And(c.names); got != c.want {
			t.Errorf("And(%q) = %q, want %q", c.names, got, c.want)
		}
	}
}

func TestOr(t *testing.T) {
	if got := Or([]string{"a", "b", "c"}); got != "a, b or c" {
		t.Errorf("Or = %q", got)
	}
}

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{Count(0, "node"), "0 nodes"},
		{Count(1, "node"), "1 node"},
		{Count(uint32(1), "object"), "1 object"},
		{Count(uint64(19), "object"), "19 objects"},
		{Count(2, "OS CA"), "2 OS CAs"},
	} {
		if tc.got != tc.want {
			t.Errorf("%q, want %q", tc.got, tc.want)
		}
	}
}

// Expected values are the output of systemd-escape from systemd 261.
func TestUnitEscape(t *testing.T) {
	for in, want := range map[string]string{
		"guest":   "guest",
		"my-vol":  `my\x2dvol`,
		"v1.2":    "v1.2",
		".a":      `\x2ea`,
		"a/b":     "a-b",
		"a b":     `a\x20b`,
		"a:b_c.d": "a:b_c.d",
		"\\":      `\x5c`,
		"é":       `\xc3\xa9`,
	} {
		if got := UnitEscape(in); got != want {
			t.Errorf("UnitEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
