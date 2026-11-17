package status

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "router", "testdata", name))
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
		got = append(got, fmt.Sprintf("%s %s %s %s up=%d rcvd=%d sent=%d bfd=%q", p.Vrf, p.Name, p.Neighbor, p.State, p.UptimeSeconds, p.PrefixesReceived, p.PrefixesSent, p.Bfd))
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

// A session a host opened from a listen range, written by hand: FRR keys it by the host's address
// and gives it the peer group of the range, so it is a session of the range's BGPPeer.
func TestParsePeersOfAListenRange(t *testing.T) {
	peers, err := ParsePeers(readTestdata(t, "neighbors-listen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Vrf != "default" || peers[0].Name != "hosts" || peers[0].Neighbor != "2001:db8:1::5" || peers[0].State != "Established" {
		t.Errorf("peers = %v, want the session of hosts with 2001:db8:1::5", peers)
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

}

// FRR 10.7 names a session's BFD status Up, Down, Admin Down or Unknown; TestParsePeers reads
// the others.
func TestBFDState(t *testing.T) {
	for in, want := range map[string]string{"Admin Down": "admin-down", "Unknown": "unknown", "": "unknown"} {
		if got := bfdState(in); got != want {
			t.Errorf("bfdState(%q) = %q, want %q", in, got, want)
		}
	}
}
