package router

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

// pdPools are the pools of the tests of records: /56s of 2001:db8:100::/40 on cust0 in the VRF
// access and on cust_x in the default VRF.
var pdPools = []PDPool{
	{VRF: "access", Link: "cust0", Range: netip.MustParsePrefix("2001:db8:100::/40"), Length: 56},
	{VRF: "default", Link: "cust_x", Range: netip.MustParsePrefix("2001:db8:100::/40"), Length: 56},
}

// writeRecords writes the records of content into dir.
func writeRecords(t *testing.T, dir string, records map[string]string) {
	t.Helper()
	for name, content := range records {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The records of delegated prefixes are routes while their leases last and a pool delegates them
// on their link to a link-local address; others are no routes: a record that ended, of another
// link, of another length, outside the pools, of an address that is no prefix's start, to an
// address that is not link-local, or that does not parse. The hook's files being written, of a
// leading dot, are none either. No file is removed.
func TestReadPDRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	later := strconv.FormatInt(now.Unix()+3600, 10)
	records := map[string]string{
		"cust0_2001:db8:100::_56":      "fe80::1 " + later + "\n",
		"cust_x_2001:db8:100:100::_56": "fe80::2 " + later + "\n",
		"cust0_2001:db8:100:200::_56":  "fe80::3 " + strconv.FormatInt(now.Unix(), 10) + "\n",
		"lan0_2001:db8:100:300::_56":   "fe80::4 " + later + "\n",
		"cust0_2001:db8:100:400::_64":  "fe80::5 " + later + "\n",
		"cust0_2001:db9::_56":          "fe80::6 " + later + "\n",
		"cust0_2001:db8:100:501::_56":  "fe80::7 " + later + "\n",
		"cust0_2001:db8:100:600::_56":  "2001:db8::8 " + later + "\n",
		"cust0_2001:db8:100:700::_56":  "fe80::9\n",
		".new.abc":                     "fe80::1\n",
	}
	writeRecords(t, dir, records)
	routes, err := ReadPDRecords(dir, pdPools, now)
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
	if left, _ := os.ReadDir(dir); len(left) != len(records) {
		t.Errorf("%d files left of %d", len(left), len(records))
	}
	if routes, err := ReadPDRecords(filepath.Join(dir, "missing"), pdPools, now); routes != nil || err != nil {
		t.Errorf("a missing directory = %v, %v", routes, err)
	}
}

// The user kea writes the records, so a record is a route only as a regular file of
// MaxPDRecordSize bytes at most: a link to a record, a FIFO, which no one writes, and a longer
// file are none, and none of them blocks the read.
func TestReadPDRecordsOfOtherFiles(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	record := "fe80::1 " + strconv.FormatInt(now.Unix()+3600, 10) + "\n"
	writeRecords(t, elsewhere, map[string]string{"cust0_2001:db8:100::_56": record})
	if err := os.Symlink(filepath.Join(elsewhere, "cust0_2001:db8:100::_56"), filepath.Join(dir, "cust0_2001:db8:100::_56")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "cust0_2001:db8:100:100::_56"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, dir, map[string]string{"cust0_2001:db8:100:200::_56": record[:len(record)-1] + strings.Repeat(" ", MaxPDRecordSize) + "\n"})
	if err := os.Mkdir(filepath.Join(dir, "cust0_2001:db8:100:300::_56"), 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var routes []PDRoute
	var err error
	go func() {
		routes, err = ReadPDRecords(dir, pdPools, now)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading the records blocked")
	}
	if len(routes) != 0 || err != nil {
		t.Errorf("routes = %v, %v", routes, err)
	}
}

// A directory of records that is a link, or that every user may write, is refused.
func TestReadPDRecordsOfOtherDirectories(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	real := t.TempDir()
	writeRecords(t, real, map[string]string{"cust0_2001:db8:100::_56": "fe80::1 " + strconv.FormatInt(now.Unix()+3600, 10) + "\n"})
	link := filepath.Join(t.TempDir(), "records")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if routes, err := ReadPDRecords(link, pdPools, now); routes != nil || err == nil {
		t.Errorf("a linked directory = %v, %v", routes, err)
	}
	if err := os.Chmod(real, 0o777); err != nil {
		t.Fatal(err)
	}
	if routes, err := ReadPDRecords(real, pdPools, now); routes != nil || err == nil || !strings.Contains(err.Error(), "only its owner writes") {
		t.Errorf("a directory every user writes = %v, %v", routes, err)
	}
}

// ReadPDRecords reads MaxPDRecords records at most and says that there were more.
func TestReadPDRecordsLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	records := map[string]string{}
	for i := range MaxPDRecords + 1 {
		records[fmt.Sprintf("cust0_2001:db8:100:%x::_64", i)] = "fe80::1 " + strconv.FormatInt(now.Unix()+3600, 10) + "\n"
	}
	writeRecords(t, dir, records)
	pools := []PDPool{{VRF: "access", Link: "cust0", Range: netip.MustParsePrefix("2001:db8:100::/40"), Length: 64}}
	routes, err := ReadPDRecords(dir, pools, now)
	if !errors.Is(err, ErrTooManyPDRecords) || len(routes) != MaxPDRecords {
		t.Errorf("%d routes, %v", len(routes), err)
	}
}
