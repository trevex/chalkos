package router

import (
	"slices"
	"strings"
	"testing"
)

// jool's instances, recorded from Jool 4.1.15, are the names of its listing, sorted; a name no
// NAT64 document gives an instance, or a line that is no instance, is an error.
func TestParseJoolInstances(t *testing.T) {
	names, err := ParseJoolInstances(readTestdata(t, "jool-instances.csv"))
	if err != nil || !slices.Equal(names, []string{"edge", "lab"}) {
		t.Errorf("instances = %v, %v", names, err)
	}
	if names, err := ParseJoolInstances(nil); err != nil || len(names) != 0 {
		t.Errorf("no instances = %v, %v", names, err)
	}
	for data, want := range map[string]string{
		"8fb43ac0,--file=/etc/x,netfilter\n": "jool lists an instance named \"--file=/etc/x\"",
		"8fb43ac0,edge\n":                    "not namespace, name and framework",
		"\"edge\n":                           "read jool's CSV",
	} {
		if _, err := ParseJoolInstances([]byte(data)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", data, err, want)
		}
	}
}

// A table's entries are its lines, with or without a state, as ICMP's sessions lack one; a line
// that is no entry of TCP, UDP or ICMP is an error.
func TestCountJoolRows(t *testing.T) {
	for name, want := range map[string]uint64{"jool-sessions-tcp.csv": 2, "jool-sessions-icmp.csv": 1} {
		if n, err := CountJoolRows(readTestdata(t, name)); err != nil || n != want {
			t.Errorf("%s: %d, %v; want %d", name, n, err, want)
		}
	}
	if n, err := CountJoolRows(nil); err != nil || n != 0 {
		t.Errorf("an empty table: %d, %v", n, err)
	}
	for _, data := range []string{"Error: Jool's kernel module is not loaded.\n", "TCP\n", "SCTP,2001:db8:ffff::2,33830,192.0.2.8,62105,0\n", "\"TCP,\n"} {
		if _, err := CountJoolRows([]byte(data)); err == nil {
			t.Errorf("%q: no error", data)
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

// jool's arguments ask for one protocol's table of an instance, with numeric addresses but for the
// pool, whose listing takes no --numeric.
func TestJoolTableArgs(t *testing.T) {
	if got := strings.Join(JoolTableArgs("edge", "session", "udp"), " "); got != "-i edge session display --csv --no-headers --udp --numeric" {
		t.Errorf("session: %s", got)
	}
	if got := strings.Join(JoolTableArgs("edge", "pool4", "icmp"), " "); got != "-i edge pool4 display --csv --no-headers --icmp" {
		t.Errorf("pool4: %s", got)
	}
}
