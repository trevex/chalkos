package router

import (
	"slices"
	"strings"
	"testing"
)

// jool's instances, recorded from Jool 4.1.15, are the names of its listing, sorted; a name no
// NAT64 document gives an instance is left out, and a line that is no instance is an error.
func TestParseJoolInstances(t *testing.T) {
	names, err := ParseJoolInstances(readTestdata(t, "jool-instances.csv"))
	if err != nil || !slices.Equal(names, []string{"edge", "lab"}) {
		t.Errorf("instances = %v, %v", names, err)
	}
	if names, err := ParseJoolInstances(nil); err != nil || len(names) != 0 {
		t.Errorf("no instances = %v, %v", names, err)
	}
	if names, err := ParseJoolInstances([]byte("8fb43ac0,--file=/etc/x,netfilter\n8fb43ac0,edge,netfilter\n9a0c11d2,Lab Box,iptables\n")); err != nil || !slices.Equal(names, []string{"edge"}) {
		t.Errorf("instances of foreign names = %v, %v", names, err)
	}
	for data, want := range map[string]string{
		"8fb43ac0,edge\n": "not namespace, name and framework",
		"\"edge\n":        "read jool's CSV",
	} {
		if _, err := ParseJoolInstances([]byte(data)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", data, err, want)
		}
	}
}

// An instance's sessions and bindings are Jool's counters of its session and BIB entries, from all
// of its statistics in the format of Jool 4.1.15; a listing without them, or with a counter that is
// no number, is an error.
func TestParseJoolStats(t *testing.T) {
	sessions, bindings, err := ParseJoolStats(readTestdata(t, "jool-stats.csv"))
	if err != nil || sessions != 3 || bindings != 2 {
		t.Errorf("stats = %d sessions, %d bindings, %v", sessions, bindings, err)
	}
	for data, want := range map[string]string{
		"":                      "lack JSTAT_SESSIONS",
		"JSTAT_BIB_ENTRIES,2\n": "lack JSTAT_SESSIONS",
		"JSTAT_SESSIONS,3\n":    "lack JSTAT_BIB_ENTRIES",
		"JSTAT_SESSIONS,x\nJSTAT_BIB_ENTRIES,2\n":      "JSTAT_SESSIONS of \"x\"",
		"JSTAT_SESSIONS,-1\nJSTAT_BIB_ENTRIES,2\n":     "JSTAT_SESSIONS of \"-1\"",
		"JSTAT_SESSIONS\n":                             "not a statistic and its value",
		"Error: Jool's kernel module is not loaded.\n": "not a statistic and its value",
		"\"JSTAT_SESSIONS,3\n":                         "read jool's CSV",
	} {
		if _, _, err := ParseJoolStats([]byte(data)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", data, err, want)
		}
	}
}

// A pool's size is the ports of each address it lists, an address a line.
func TestParseJoolPool4(t *testing.T) {
	if n, err := ParseJoolPool4(readTestdata(t, "jool-pool4-tcp.csv")); err != nil || n != 2*4535+32767 {
		t.Errorf("size = %d, %v", n, err)
	}
	if n, err := ParseJoolPool4(nil); err != nil || n != 0 {
		t.Errorf("an empty pool: %d, %v", n, err)
	}
	for _, data := range []string{"0,TCP,192.0.2.8\n", "0,TCP,192.0.2.8,65535,61001,1059,0\n", "0,TCP,192.0.2.8,x,1,1059,0\n", "0,TCP,192.0.2.8,1,65536,1059,0\n", "0,TCP,192.0.2.8,-1,2,1059,0\n"} {
		if _, err := ParseJoolPool4([]byte(data)); err == nil {
			t.Errorf("%q: no error", data)
		}
	}
}

// jool's arguments ask for all of an instance's statistics, and for one protocol's pool.
func TestJoolArgs(t *testing.T) {
	if got := strings.Join(JoolStatsArgs("edge"), " "); got != "-i edge stats display --csv --no-headers --all" {
		t.Errorf("stats: %s", got)
	}
	if got := strings.Join(JoolPool4Args("edge", "icmp"), " "); got != "-i edge pool4 display --csv --no-headers --icmp" {
		t.Errorf("pool4: %s", got)
	}
}
