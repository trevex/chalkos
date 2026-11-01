package router

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The ranges of delegated prefixes of a DHCPv6 config of the output kea are its subnets' pd-pools,
// each with the subnet's link and the server's VRF.
func TestParseKea6Pools(t *testing.T) {
	got, err := ParseKea6Pools("access", []byte(`{"Dhcp6": {"subnet6": [
		{"interface": "lan0", "subnet": "2001:db8:1::/64"},
		{"interface": "cust0", "pd-pools": [{"prefix": "2001:db8:100::", "prefix-len": 40, "delegated-len": 56}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []PDPool{{VRF: "access", Link: "cust0", Range: netip.MustParsePrefix("2001:db8:100::/40"), Length: 56}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pools = %v, want %v", got, want)
	}
	if _, err := ParseKea6Pools("default", []byte(`{"Dhcp6": {"subnet6": [{"pd-pools": [{"prefix": "x", "prefix-len": 40}]}]}}`)); err == nil {
		t.Error("a pool of no address parsed")
	}
}

// The records of delegated prefixes are routes while their leases last and a pool delegates them
// on their link to a link-local address; others are stale: a record that ended, of another link,
// of another length, outside the pools, of an address that is no prefix's start, to an address
// that is not link-local, or that does not parse. The hook's files being written, of a leading
// dot, are neither.
func TestReadPDRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	later := strconv.FormatInt(now.Unix()+3600, 10)
	for name, content := range map[string]string{
		"cust0_2001:db8:100::_56":      "fe80::1 " + later,
		"cust_x_2001:db8:100:100::_56": "fe80::2 " + later,
		"cust0_2001:db8:100:200::_56":  "fe80::3 " + strconv.FormatInt(now.Unix(), 10),
		"lan0_2001:db8:100:300::_56":   "fe80::4 " + later,
		"cust0_2001:db8:100:400::_64":  "fe80::5 " + later,
		"cust0_2001:db9::_56":          "fe80::6 " + later,
		"cust0_2001:db8:100:501::_56":  "fe80::7 " + later,
		"cust0_2001:db8:100:600::_56":  "2001:db8::8 " + later,
		"cust0_2001:db8:100:700::_56":  "fe80::9",
		".new.abc":                     "fe80::1",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pools := []PDPool{
		{VRF: "access", Link: "cust0", Range: netip.MustParsePrefix("2001:db8:100::/40"), Length: 56},
		{VRF: "default", Link: "cust_x", Range: netip.MustParsePrefix("2001:db8:100::/40"), Length: 56},
	}
	routes, stale, err := ReadPDRecords(dir, pools, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []PDRoute{
		{VRF: "access", Link: "cust0", Prefix: netip.MustParsePrefix("2001:db8:100::/56"), Via: netip.MustParseAddr("fe80::1"), Ends: now.Add(time.Hour)},
		{VRF: "default", Link: "cust_x", Prefix: netip.MustParsePrefix("2001:db8:100:100::/56"), Via: netip.MustParseAddr("fe80::2"), Ends: now.Add(time.Hour)},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Errorf("routes = %v, want %v", routes, want)
	}
	var names []string
	for _, s := range stale {
		names = append(names, filepath.Base(s))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"cust0_2001:db8:100:200::_56", "cust0_2001:db8:100:400::_64", "cust0_2001:db8:100:501::_56", "cust0_2001:db8:100:600::_56", "cust0_2001:db8:100:700::_56", "cust0_2001:db9::_56", "lan0_2001:db8:100:300::_56"}) {
		t.Errorf("stale = %v", names)
	}
	if routes, stale, err := ReadPDRecords(filepath.Join(dir, "missing"), pools, now); routes != nil || stale != nil || err != nil {
		t.Errorf("a missing directory = %v, %v, %v", routes, stale, err)
	}
}
