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
