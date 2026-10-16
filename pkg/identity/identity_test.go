package identity

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStaticAddresses(t *testing.T) {
	id := Identity{Network: map[string]any{"networks": map[string]any{
		"20-b": map[string]any{"address": []any{"10.0.1.1/24"}},
		"10-a": map[string]any{"address": []any{"10.0.0.1/24", "fd00::1/64"}},
		"30-c": map[string]any{"DHCP": "yes"},
	}}}
	got := id.StaticAddresses()
	want := []string{"10.0.0.1", "fd00::1", "10.0.1.1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("StaticAddresses() = %v, want %v", got, want)
	}
}

// A server without "nts" is authenticated, as the Nix option's default says.
func TestTimeServerNTSDefault(t *testing.T) {
	var time Time
	if err := json.Unmarshal([]byte(`{"servers": [{"host": "a"}, {"host": "b", "nts": false}, {"host": "c", "nts": true}]}`), &time); err != nil {
		t.Fatal(err)
	}
	sources, err := time.ChronySources()
	if err != nil {
		t.Fatal(err)
	}
	if want := "server a iburst nts\nserver b iburst\nserver c iburst nts\n"; sources != want {
		t.Errorf("sources = %q, want %q", sources, want)
	}
	if err := json.Unmarshal([]byte(`{"servers": [{"host": "a", "secure": true}]}`), &time); err == nil {
		t.Error("accepted an unknown field of a time server")
	}
}

// A node's fixed addresses must be addresses, without prefix lengths.
func TestKubernetesCheckNodeIPs(t *testing.T) {
	if err := (Kubernetes{NodeIPs: []string{"10.0.0.11", "fd00::11"}}).CheckNodeIPs(); err != nil {
		t.Errorf("addresses: %v", err)
	}
	if err := (Kubernetes{NodeIPs: []string{"10.0.0.11/24"}}).CheckNodeIPs(); err == nil || !strings.Contains(err.Error(), "10.0.0.11/24") {
		t.Errorf("a prefix: %v", err)
	}
}
