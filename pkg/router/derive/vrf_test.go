package derive

import (
	"reflect"
	"strings"
	"testing"
)

// The links of the Network documents are in the VRF whose document or whose Network's own VRF=
// holds them; the Networks are in the VRF of the links they are first to match.
func TestVRFLinks(t *testing.T) {
	byKind := map[string]any{"Network": []any{
		document(t, "Network", "10-swp", `{"match": {"name": "swp1 swp2"}}`),
		document(t, "Network", "20-swp", `{"match": {"name": "swp*"}}`),
		document(t, "Network", "30-eth1", `{"match": {"name": "eth1"}, "network": {"vrf": "blue"}}`),
		document(t, "Network", "40-eth0", `{"match": {"name": "eth0"}}`),
	}}
	got, err := vrfLinks([]Named[VRFSpec]{{"tenant", VRFSpec{Table: 100, Interfaces: []string{"swp*"}}}}, networkLinks(byKind), byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"swp1": "tenant", "swp2": "tenant", "eth1": "blue", "eth0": DefaultVRF} {
		if vrf, err := got.LinkVRF(link); err != nil || vrf != want {
			t.Errorf("linkVRF(%s) = %q, %v, want %q", link, vrf, err, want)
		}
	}
	if want := map[string]string{"10-swp": "tenant", "20-swp": "tenant", "30-eth1": "blue"}; !reflect.DeepEqual(got.Networks, want) {
		t.Errorf("networks = %v, want %v", got.Networks, want)
	}
}

// networkd puts a link in the VRF of the Network it picks, which a Network whose other [Match]
// keys may not match leaves open unless a Network that matches the link by Name= alone follows.
func TestLinkVRFNarrowed(t *testing.T) {
	mac := `, "macAddress": "02:00:00:00:00:01"`
	byKind := map[string]any{"Network": []any{
		document(t, "Network", "10-swp", `{"match": {"name": "swp1 swp2"`+mac+`}}`),
		document(t, "Network", "20-swp2", `{"match": {"name": "swp2"}}`),
		document(t, "Network", "30-eth", `{"match": {"name": "eth0"`+mac+`}}`),
	}}
	m, err := vrfLinks([]Named[VRFSpec]{{"tenant", VRFSpec{Table: 100, Interfaces: []string{"swp*"}}}}, networkLinks(byKind), byKind)
	if err != nil {
		t.Fatal(err)
	}
	for link, want := range map[string]string{"swp2": "tenant", "eth0": DefaultVRF, "lo": DefaultVRF} {
		if got, err := m.LinkVRF(link); err != nil || got != want {
			t.Errorf("linkVRF(%s) = %q, %v, want %q", link, got, err, want)
		}
	}
	want := "networkd puts swp1 in VRF tenant only if the other [Match] keys of Network 10-swp match it"
	if _, err := m.LinkVRF("swp1"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("linkVRF(swp1): err = %v, want %q", err, want)
	}
}

// Other [Match] keys narrow a Network unless an empty value resets them, as systemd reads it.
func TestOtherMatchKeys(t *testing.T) {
	mac := "02:00:00:00:00:01"
	for _, c := range []struct {
		match any
		want  bool
	}{
		{map[string]any{"name": "eth1"}, false},
		{map[string]any{"name": "eth1", "macAddress": mac}, true},
		{map[string]any{"name": "eth1", "macAddress": ""}, false},
		{map[string]any{"name": "eth1", "macAddress": " \t"}, false},
		{map[string]any{"name": "eth1", "macAddress": []any{}}, false},
		{map[string]any{"name": "eth1", "macAddress": []any{mac, ""}}, false},
		{map[string]any{"name": "eth1", "macAddress": []any{"", mac}}, true},
		{[]any{map[string]any{"macAddress": mac}, map[string]any{"macAddress": ""}}, false},
		{[]any{map[string]any{"macAddress": ""}, map[string]any{"driver": "e1000"}}, true},
	} {
		if got := otherMatchKeys(map[string]any{"match": c.match}); got != c.want {
			t.Errorf("otherMatchKeys(%v) = %v, want %v", c.match, got, c.want)
		}
	}
}
