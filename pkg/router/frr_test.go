package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The selected paths of show bgp ipv6 unicast detail json, recorded from FRR 10.7 on an inner
// router behind a fabric edge: the default route over both sessions, the best first, with its
// local preference and communities, a network the router originates, and no path that lost.
// The IPv4 paths carry an IPv6 next hop (RFC 8950) and an address peer's AS path and MED.
func TestParseBGPRoutes(t *testing.T) {
	if _, err := ParseRoutes(nil); err == nil || !strings.Contains(err.Error(), "bgpd did not answer") {
		t.Errorf("err of no paths = %v", err)
	}
	describe := func(rs []*BGPRoute) string {
		var lines []string
		for _, r := range rs {
			pref := "-"
			if r.LocalPref != nil {
				pref = fmt.Sprint(*r.LocalPref)
			}
			lines = append(lines, fmt.Sprintf("%s best=%v path=%q peer=%q via=%v pref=%s med=%d comm=%v", r.Prefix, r.Best, r.ASPath, r.Peer, r.NextHops, pref, r.MED, r.Communities))
		}
		return strings.Join(lines, "\n")
	}
	for _, c := range []struct {
		file string
		want []string
	}{
		{"ipv6-detail.json", []string{
			`::/0 best=true path="64512" peer="eth2" via=[fe80::e06b:dcff:fe54:f465] pref=200 med=0 comm=[64512:1 64512:1:2]`,
			`::/0 best=false path="64512" peer="eth1" via=[fe80::b817:4cff:fe8b:9cf] pref=200 med=0 comm=[]`,
			`2001:db8:2::/48 best=true path="" peer="" via=[::] pref=- med=0 comm=[]`,
			`2001:db8:ffff::1/128 best=true path="64512" peer="eth2" via=[fe80::e06b:dcff:fe54:f465] pref=200 med=0 comm=[]`,
			`2001:db8:ffff::1/128 best=false path="64512" peer="eth1" via=[fe80::b817:4cff:fe8b:9cf] pref=200 med=0 comm=[]`,
		}},
		{"ipv4-detail.json", []string{
			`192.0.2.1/32 best=true path="64512" peer="eth2" via=[fe80::e06b:dcff:fe54:f465] pref=- med=0 comm=[]`,
			`198.51.100.0/24 best=true path="65001 65002" peer="203.0.113.254" via=[203.0.113.254] pref=- med=50 comm=[65001:100]`,
		}},
		{"", nil},
	} {
		data := []byte(`{"warning": "Default BGP instance not found"}`)
		if c.file != "" {
			data = readTestdata(t, c.file)
		}
		routes, err := ParseRoutes(data)
		if err != nil {
			t.Fatal(err)
		}
		if got := describe(routes); got != strings.Join(c.want, "\n") {
			t.Errorf("%s:\n%s\nwant:\n%s", c.file, got, strings.Join(c.want, "\n"))
		}
	}
}

// An instance's paths of show bgp ipv6 unicast detail json, recorded from FRR 10.7.1 on a router
// whose default instance imports the VRF fabric's routes: its own network, and a path FRR marks
// local but imported from fabric, which keeps its AS path and next hop and names the VRF.
func TestParseImportedRoutes(t *testing.T) {
	routes, err := ParseRoutes(readTestdata(t, "ipv6-detail-imported.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range routes {
		got = append(got, fmt.Sprintf("%s as=%q peer=%q from=%q hops=%v best=%v", r.Prefix, r.ASPath, r.Peer, r.ImportedFrom, r.NextHops, r.Best))
	}
	want := []string{
		`2001:db8:ffff::1/128 as="" peer="" from="" hops=[::] best=true`,
		`2001:db8:ffff::2/128 as="64513" peer="" from="fabric" hops=[fe80::5054:ff:fe12:102] best=true`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A path FRR imported without naming the VRF of its next hop keeps the route distinguisher it
	// came from, so it still reads as imported.
	unnamed := strings.Replace(string(readTestdata(t, "ipv6-detail-imported.json")), `"nhVrfName":"fabric",`, "", 1)
	if routes, err := ParseRoutes([]byte(unnamed)); err != nil || len(routes) != 2 || routes[1].ImportedFrom != "192.0.2.1:2" || routes[1].Peer != "" {
		t.Errorf("an imported path without nhVrfName: %v, %v", routes, err)
	}
	// A VRF without an instance answers with a warning and no paths.
	if routes, err := ParseRoutes([]byte(`{"warning":"View/Vrf is unknown"}`)); err != nil || len(routes) != 0 {
		t.Errorf("a VRF without an instance: %v, %v", routes, err)
	}
}

// A route the instance redistributes from the kernel, as FRR 10.7.1 prints it: sourced but not
// local, from the peer ::. It reads as the router's own path, with its communities.
func TestParseRedistributedRoute(t *testing.T) {
	data := []byte(`{"routes":{"2001:db8:fff0::/48":[{"aspath":{"string":"Local","length":0},"origin":"incomplete",
		"metric":0,"valid":true,"sourced":true,"community":{"string":"no-advertise","list":["noAdvertise"]},
		"largeCommunity":{"string":"4200000000:0:1","list":["4200000000:0:1"]},"bestpath":{"overall":true},
		"nexthops":[{"ip":"::","afi":"ipv6","used":true}],"peer":{"peerId":"::","routerId":"192.0.2.1"}}]}}`)
	routes, err := ParseRoutes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes: %v", routes)
	}
	r := routes[0]
	if r.Prefix != "2001:db8:fff0::/48" || !r.Best || r.ASPath != "" || r.Peer != "" || r.ImportedFrom != "" ||
		!slices.Equal(r.Communities, []string{"noAdvertise", "4200000000:0:1"}) {
		t.Errorf("redistributed route = %+v", r)
	}
}
