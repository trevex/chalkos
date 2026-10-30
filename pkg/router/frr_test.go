package router

import (
	"fmt"
	"os"
	"path/filepath"
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

// The peers of show bgp vrf all neighbors json: the default VRF's sessions, recorded from FRR
// 10.7 on a fabric edge whose session on eth2 BFD then ended, and a tenant VRF's session on swp1,
// written by hand in the shape FRR 10.7.1 gave a VRF's session in the lab. One entry per
// session, the default VRF's first, by BGPPeer and neighbour, with its VRF, state, the uptime of
// an established one, its routes of every family and its BFD state.
func TestParsePeers(t *testing.T) {
	peers, err := ParsePeers(readTestdata(t, "neighbors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range peers {
		got = append(got, fmt.Sprintf("%s %s %s %s up=%d rcvd=%d sent=%d bfd=%q", p.VRF, p.Name, p.Neighbor, p.State, p.UptimeSeconds, p.PrefixesReceived, p.PrefixesSent, p.BFD))
	}
	want := []string{
		`default fabric eth1 Established up=3723 rcvd=1 sent=2 bfd="up"`,
		`default fabric eth2 Idle up=0 rcvd=0 sent=0 bfd="down"`,
		`default transit 203.0.113.254 Active up=0 rcvd=0 sent=0 bfd=""`,
		`tenant leaves swp1 Established up=21 rcvd=1 sent=2 bfd=""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("peers:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// FRR prints an object per VRF without sessions, or {} without any instance; a VRF's vrfId and
// vrfName are no sessions, also the vrfId -1 of an instance whose VRF the kernel lacks, and a
// session of no BGPPeer's peer group is left out. What is no JSON is an error.
func TestParsePeersWithout(t *testing.T) {
	for _, data := range []string{"{\n}\n", `{}`, `{"default": {"vrfId": 0, "vrfName": "default"}}`, `{"tenant": {"vrfId": -1, "vrfName": "tenant"}}`, `{"default": {"vrfId": 0, "vrfName": "default", "eth9": {"bgpState": "Idle"}}}`} {
		if peers, err := ParsePeers([]byte(data)); err != nil || len(peers) != 0 {
			t.Errorf("%s: %v, %v", data, peers, err)
		}
	}
	if _, err := ParsePeers([]byte("% Unknown command")); err == nil || !strings.Contains(err.Error(), "read FRR's BGP neighbours") {
		t.Errorf("err = %v", err)
	}
	// vtysh prints nothing while bgpd is not running.
	if _, err := ParsePeers([]byte(" \n")); err == nil || !strings.Contains(err.Error(), "bgpd did not answer") {
		t.Errorf("err of no output = %v", err)
	}
	if _, err := ParseRoutes(nil); err == nil || !strings.Contains(err.Error(), "bgpd did not answer") {
		t.Errorf("err of no paths = %v", err)
	}
}

// The selected paths of show bgp ipv6 unicast detail json, recorded from FRR 10.7 on an inner
// router behind a fabric edge: the default route over both sessions, the best first, with its
// local preference and communities, a network the router originates, and no path that lost.
// The IPv4 paths carry an IPv6 next hop (RFC 8950) and an address peer's AS path and MED.
func TestParseBGPRoutes(t *testing.T) {
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

// FRR 10.7 names a session's BFD status Up, Down, Admin Down or Unknown.
func TestBFDState(t *testing.T) {
	for in, want := range map[string]string{"Up": "up", "Down": "down", "Admin Down": "admin-down", "Unknown": "unknown", "": "unknown"} {
		if got := bfdState(in); got != want {
			t.Errorf("bfdState(%q) = %q, want %q", in, got, want)
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
