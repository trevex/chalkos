package derive

import (
	"slices"
	"testing"
)

// The links the Network documents name are the plain names of their [Match] sections' Name=; a
// value whose first name starts with ! inverts all of its names, as systemd reads it, and names
// none.
func TestNetworkLinks(t *testing.T) {
	network := func(match any) any { return map[string]any{"spec": map[string]any{"match": match}} }
	got := networkLinks(map[string]any{"Network": []any{
		network(map[string]any{"name": "swp1 swp2"}),
		network([]any{map[string]any{"name": []any{"eth0", "!eth1 eth2"}}, map[string]any{"name": "swp* eth3 !eth4"}}),
		network(map[string]any{"name": "!eth5 eth6"}),
		network(map[string]any{"name": []any{"eth7", ""}}),
	}})
	if want := []string{"eth0", "eth3", "swp1", "swp2"}; !slices.Equal(got, want) {
		t.Errorf("networkLinks = %v, want %v", got, want)
	}
}

// Name= matches as systemd's does: globs per word, split at spaces, tabs and line breaks, a
// first word with ! negating every word, a later word with ! negating itself, a value of only
// whitespace resetting the values before it, and words systemd takes for no interface name, an
// interface index among them, left out. A backslash and a [ in a bracket expression, which
// fnmatch and path.Match read differently, are refused.
func TestNameMatch(t *testing.T) {
	for _, c := range []struct {
		names   any
		matches []string
		misses  []string
	}{
		{"eth0", []string{"eth0"}, []string{"eth1", "eth00"}},
		{"eth* swp[12]", []string{"eth0", "swp2"}, []string{"swp3", "lo"}},
		{"swp[!1]", []string{"swp2", "swp!"}, []string{"swp1", "swp"}},
		{"!eth0 eth1", []string{"lo", "eth2"}, []string{"eth0", "eth1"}},
		{"eth* !eth1", []string{"eth0"}, []string{"eth1", "lo"}},
		{[]any{"eth0", "eth1"}, []string{"eth0", "eth1"}, []string{"eth2"}},
		{[]any{"eth0", "", "eth1"}, []string{"eth1"}, []string{"eth0"}},
		{[]any{"eth0", ""}, []string{"eth0", "lo"}, nil},
		{"eth0:1 eth1", []string{"eth1"}, []string{"eth0:1", "lo"}},
		{nil, []string{"eth0"}, nil},
		{[]any{"eth0", " \t "}, []string{"eth0", "lo"}, nil},
		{"eth1 eth0\veth2", []string{"eth1"}, []string{"eth0", "eth2", "eth0\veth2"}},
		{"0x1f 0b1 0o7 +5 017 08 0 eth1", []string{"eth1"}, []string{"0x1f", "0b1", "0o7", "+5", "017", "08", "0"}},
		{"-5 0x 0x100000000 eth1", []string{"-5", "0x", "0x100000000", "eth1"}, []string{"lo"}},
		{"0x7fffffff 0x80000000 eth1", []string{"0x80000000", "eth1"}, []string{"0x7fffffff"}},
		{"0b0b1 +0b1 0b+1 eth1", []string{"eth1"}, []string{"0b0b1", "+0b1", "0b+1"}},
	} {
		m, err := parseNameMatch("n", map[string]any{"match": map[string]any{"name": c.names}})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range c.matches {
			if !m.Matches(l) {
				t.Errorf("Name=%v misses %s", c.names, l)
			}
		}
		for _, l := range c.misses {
			if m.Matches(l) {
				t.Errorf("Name=%v matches %s", c.names, l)
			}
		}
	}
	for _, name := range []string{"eth[0", `eth\0`, `eth1 eth\`, "eth[[=a=]]", "eth[[.a.]]", "eth[a[]"} {
		if _, err := parseNameMatch("n", map[string]any{"match": map[string]any{"name": name}}); err == nil {
			t.Errorf("parseNameMatch takes Name=%s", name)
		}
	}
}
