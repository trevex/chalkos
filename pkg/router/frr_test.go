package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The peers of show bgp neighbors json, recorded from FRR 10.7 on a fabric edge whose session on
// eth2 BFD then ended: one entry per session, by BGPPeer and neighbour, with its state, the
// uptime of an established one, its routes of every family and its BFD state.
func TestParsePeers(t *testing.T) {
	peers, err := ParsePeers(readTestdata(t, "neighbors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range peers {
		got = append(got, fmt.Sprintf("%s %s %s up=%d rcvd=%d sent=%d bfd=%q", p.Name, p.Neighbor, p.State, p.UptimeSeconds, p.PrefixesReceived, p.PrefixesSent, p.Bfd))
	}
	want := []string{
		`fabric eth1 Established up=3723 rcvd=1 sent=2 bfd="up"`,
		`fabric eth2 Idle up=0 rcvd=0 sent=0 bfd="down"`,
		`transit 203.0.113.254 Active up=0 rcvd=0 sent=0 bfd=""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("peers:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// FRR prints {} without a BGP instance, and a session of no BGPPeer's peer group is left out;
// what is no JSON is an error.
func TestParsePeersWithout(t *testing.T) {
	for _, data := range []string{`{}`, `{"eth9": {"bgpState": "Idle"}}`} {
		if peers, err := ParsePeers([]byte(data)); err != nil || len(peers) != 0 {
			t.Errorf("%s: %v, %v", data, peers, err)
		}
	}
	if _, err := ParsePeers([]byte("% Unknown command")); err == nil || !strings.Contains(err.Error(), "read FRR's BGP neighbours") {
		t.Errorf("err = %v", err)
	}
}

// The selected paths of show bgp ipv6 unicast detail json, recorded from FRR 10.7 on an inner
// router behind a fabric edge: the default route over both sessions, the best first, with its
// local preference and communities, a network the router originates, and no path that lost.
// The IPv4 paths carry an IPv6 next hop (RFC 8950) and an address peer's AS path and MED.
func TestParseBGPRoutes(t *testing.T) {
	describe := func(rs []*nodev1.BGPRoute) string {
		var lines []string
		for _, r := range rs {
			pref := "-"
			if r.LocalPref != nil {
				pref = fmt.Sprint(*r.LocalPref)
			}
			lines = append(lines, fmt.Sprintf("%s best=%v path=%q peer=%q via=%v pref=%s med=%d comm=%v", r.Prefix, r.Best, r.AsPath, r.Peer, r.NextHops, pref, r.Med, r.Communities))
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
