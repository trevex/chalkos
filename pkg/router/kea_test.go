package router

import (
	"encoding/json"
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

// The control sockets of the default VRF's server and of a VRF's, as the output kea renders them.
func TestKeaSocket(t *testing.T) {
	if got := KeaSocket("/run/kea", "default"); got != "/run/kea/dhcp4.sock" {
		t.Errorf("the default VRF's socket = %s", got)
	}
	if got := KeaSocket("/run/kea", "guest"); got != "/run/kea/dhcp4-guest.sock" {
		t.Errorf("guest's socket = %s", got)
	}
}
