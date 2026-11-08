package router

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

// Kea 3.2.0's statistic-get-all of a server of one subnet that leased the reservation of a
// printer and an address of its pool of 100 gives each subnet's newest counts of leased and pool
// addresses by its number, leaving out the counts of the server, of the pools and the others.
func TestParseKeaStatistics(t *testing.T) {
	data, err := os.ReadFile("testdata/kea-statistic-get-all.json")
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatal(err)
	}
	got, err := ParseKeaStatistics(answer.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[uint32]KeaLeases{3221225984: {Assigned: 2, Total: 100}}; !reflect.DeepEqual(got, want) {
		t.Errorf("statistics = %v, want %v", got, want)
	}
	if _, err := ParseKeaStatistics(json.RawMessage(`{"subnet[1].assigned-addresses": [["two", "2026-11-01 10:00:00.000000"]]}`)); err == nil {
		t.Error("a count of no number parsed")
	}
}

// The subnets of a config of the output kea are its subnet4's numbers, ranges and documents'
// names, in its order.
func TestParseKeaSubnets(t *testing.T) {
	got, err := ParseKeaSubnets([]byte(`{"Dhcp4": {"subnet4": [
		{"id": 3221225984, "subnet": "192.0.2.0/24", "user-context": {"name": "office"}, "pools": []},
		{"id": 3325256704, "subnet": "198.51.100.0/24", "user-context": {"name": "guests"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []KeaSubnet{{3221225984, "office", "192.0.2.0/24"}, {3325256704, "guests", "198.51.100.0/24"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subnets = %v, want %v", got, want)
	}
	if _, err := ParseKeaSubnets([]byte("{")); err == nil {
		t.Error("a config of no JSON parsed")
	}
}

// The links of a DHCPv4 and of a DHCPv6 config, and none of a config without them.
func TestParseKeaInterfaces(t *testing.T) {
	for data, want := range map[string][]string{
		`{"Dhcp4": {"interfaces-config": {"interfaces": ["lan0", "vlan20"]}}}`: {"lan0", "vlan20"},
		`{"Dhcp6": {"interfaces-config": {"interfaces": ["vlan31"]}}}`:         {"vlan31"},
		`{"Dhcp4": {}}`: nil,
	} {
		if got, err := ParseKeaInterfaces([]byte(data)); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseKeaInterfaces(%s) = %v, %v, want %v", data, got, err, want)
		}
	}
	if _, err := ParseKeaInterfaces([]byte("{")); err == nil {
		t.Error("a config of no JSON parsed")
	}
}

// The control sockets of the default VRF's server and of a VRF's, as the output kea renders them.
func TestKeaSocket(t *testing.T) {
	for _, c := range [][3]string{
		{"dhcp4", "default", "/run/kea/dhcp4.sock"},
		{"dhcp4", "guest", "/run/kea/dhcp4-guest.sock"},
		{"dhcp6", "default", "/run/kea/dhcp6.sock"},
		{"dhcp6", "guest", "/run/kea/dhcp6-guest.sock"},
	} {
		if got := KeaSocket("/run/kea", c[0], c[1]); got != c[2] {
			t.Errorf("the socket of %s of %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

// Kea 3.2.0's statistic-get-all of a DHCPv6 server of one subnet that leased an address of its
// pool of 256 and delegated a prefix of its 256 gives the subnet's counts of both, leaving out the
// counts of the server and of the pools. A count past 64 bits, as of the addresses of a pool of a
// /64, is the largest of 64.
func TestParseKea6Statistics(t *testing.T) {
	data, err := os.ReadFile("testdata/kea6-statistic-get-all.json")
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatal(err)
	}
	got, err := ParseKeaStatistics(answer.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[uint32]KeaLeases{2387838497: {Assigned: 1, Total: 256, Delegated: 1, Delegable: 256}}; !reflect.DeepEqual(got, want) {
		t.Errorf("statistics = %v, want %v", got, want)
	}
	got, err = ParseKeaStatistics(json.RawMessage(`{"subnet[7].total-nas": [[36893488147419103230, "2026-11-01 10:00:00.000000"]]}`))
	if err != nil || got[7].Total != math.MaxUint64 {
		t.Errorf("a pool past 64 bits = %v, %v", got, err)
	}
}
