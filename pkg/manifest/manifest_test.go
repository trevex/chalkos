package manifest

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
)

func TestDecodeGoldenManifest(t *testing.T) {
	f, err := os.Open("../../test/fixtures/homelab-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.Cluster.Name != "homelab" || m.Cluster.Endpoint != "https://10.0.0.11:6443" {
		t.Errorf("cluster = %+v", m.Cluster)
	}
	if got := m.Roles["worker"].Image; got != "roles.worker.image" {
		t.Errorf("worker image = %q", got)
	}
	w1 := m.Nodes["w1"]
	if w1.Role != "worker" || w1.Identity.Hostname != "w1" {
		t.Errorf("w1 = %+v", w1)
	}
	if len(w1.Identity.Taints) != 1 || w1.Identity.Taints[0].Effect != "NoSchedule" {
		t.Errorf("w1 taints = %+v", w1.Identity.Taints)
	}
	var rack struct {
		Location string `json:"location"`
	}
	if err := json.Unmarshal(w1.Identity.Extensions["rack"], &rack); err != nil {
		t.Fatalf("unmarshal rack extension: %v", err)
	}
	if rack.Location != "rack-a/u14" {
		t.Errorf("w1 rack extension location = %q", rack.Location)
	}

	if got := m.Nodes["cp1"].Identity.NetworkUnits["10-uplink.network"]; !strings.HasPrefix(got, "[Match]\nName=enp1s0\n") {
		t.Errorf("cp1 uplink unit = %q", got)
	}
	if got := m.Nodes["cp1"].Identity.Storage.Disks["system"].Ref.Path; got != "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001" {
		t.Errorf("cp1 system disk = %q", got)
	}
	st := w1.Identity.Storage
	if got, want := st.Disks["longhorn"].Ref, (storage.Ref{Selector: storage.Selector{Model: "Samsung SSD 870*", Type: "ssd"}}); got != want {
		t.Errorf("w1 longhorn disk = %+v, want %+v", got, want)
	}
	if got, want := st.Volumes["longhorn"], (storage.Volume{Disk: "longhorn", Label: "longhorn", Format: "xfs", MountPoint: "/var/lib/longhorn", Encryption: "tpm2"}); got != want {
		t.Errorf("w1 longhorn volume = %+v, want %+v", got, want)
	}
	if st.Volumes["var"].Size != "200G" || st.Fallback != "recovery-key" || st.Encryption != "tpm2" {
		t.Errorf("w1 storage = %+v", st)
	}
	// A node validates the section it receives; what the cluster definition renders passes.
	for name, n := range m.Nodes {
		if err := n.Identity.Storage.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"schemaVersion": 1}`))
	if err == nil || !strings.Contains(err.Error(), "older than the cluster definition") {
		t.Fatalf("err = %v, want unsupported version error", err)
	}
}

func TestDecodeRejectsOlderVersion(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"schemaVersion": -1}`))
	if err == nil || !strings.Contains(err.Error(), "update chalkos") {
		t.Fatalf("err = %v, want older manifest format error", err)
	}
}

func TestDecodeRequiresVersion(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"cluster": {"name": "homelab"}}`))
	if err == nil || !strings.Contains(err.Error(), "manifest has no schemaVersion") {
		t.Fatalf("err = %v, want missing schemaVersion error", err)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"schemaVersion": 0, "surprise": true}`))
	if err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestDecodeRejectsUnknownStorageFields(t *testing.T) {
	for _, storage := range []string{
		`{"disks": {"d": {"ref": {"model": "x", "vendor": "y"}, "seed": "s", "repart": {}}}, "volumes": {}, "fallback": "none"}`,
		`{"disks": {}, "volumes": {}, "fallback": "none", "surprise": 1}`,
	} {
		_, err := Decode(strings.NewReader(`{"schemaVersion": 0, "nodes": {"n1": {"role": "r", "identity": {"storage": ` + storage + `}}}}`))
		if err == nil {
			t.Errorf("accepted storage %s", storage)
		}
	}
}

func TestDecodeKubernetes(t *testing.T) {
	f, err := os.Open("../../test/fixtures/homelab-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.Roles["controlplane"].Kind != KindControlPlane || m.Roles["worker"].Kind != KindWorker {
		t.Errorf("roles = %+v", m.Roles)
	}
	if k := m.Nodes["cp1"].Identity.Kubernetes; k == nil || k.NodeName != "cp1" || !slices.Equal(k.NodeIPs, []string{"10.0.0.11"}) || k.ValidSubnets != nil {
		t.Errorf("cp1 kubernetes = %+v", k)
	}
	if k := m.Nodes["w1"].Identity.Kubernetes; k == nil || k.NodeName != "w1" || len(k.NodeIPs) != 0 || k.ValidSubnets != nil {
		t.Errorf("w1 kubernetes = %+v", k)
	}
}

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

func TestDecodeValidSubnets(t *testing.T) {
	m, err := Decode(strings.NewReader(`{"schemaVersion": 0, "nodes": {"n1": {"role": "r", "identity": {
	  "kubernetes": {"nodeName": "n1", "nodeIPs": [], "validSubnets": ["192.168.100.0/24", "!192.168.100.1/32"]}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	k := m.Nodes["n1"].Identity.Kubernetes
	if k == nil || len(k.NodeIPs) != 0 || !slices.Equal(k.ValidSubnets, []string{"192.168.100.0/24", "!192.168.100.1/32"}) {
		t.Errorf("kubernetes = %+v", k)
	}
}

// A subnet the node would refuse is refused before chalkctl installs or applies anything.
func TestDecodeRejectsInvalidSubnets(t *testing.T) {
	for _, bad := range []string{"999.1.1.1/40", "a/1", "::::/999", "10.0.0.0/33", "fd00::/129", "!::ffff:10.0.0.10/128"} {
		_, err := Decode(strings.NewReader(`{"schemaVersion": 0, "nodes": {"n1": {"role": "r", "identity": {
		  "kubernetes": {"nodeName": "n1", "nodeIPs": [], "validSubnets": ["10.0.0.0/8", "` + bad + `"]}}}}}`))
		if err == nil || !strings.Contains(err.Error(), "node n1") || !strings.Contains(err.Error(), bad) {
			t.Errorf("validSubnets %q: err = %v", bad, err)
		}
	}
	_, err := Decode(strings.NewReader(`{"schemaVersion": 0, "nodes": {"n1": {"role": "r", "identity": {
	  "kubernetes": {"nodeName": "n1", "nodeIPs": ["10.0.0.11/24"], "validSubnets": null}}}}}`))
	if err == nil || !strings.Contains(err.Error(), "node n1") || !strings.Contains(err.Error(), "10.0.0.11/24") {
		t.Errorf("nodeIPs: err = %v", err)
	}
}

func TestDecodeRefusesTimeServers(t *testing.T) {
	for _, host := range []string{"a b", "x\\nserver y"} {
		m := `{"schemaVersion": 0, "cluster": {"name": "lab"}, "roles": {}, "nodes": {"n1": {"role": "r", "identity": {"time": {"servers": [{"host": "` + host + `"}]}}}}}`
		if _, err := Decode(strings.NewReader(m)); err == nil || !strings.Contains(err.Error(), "time server") {
			t.Errorf("host %q: %v", host, err)
		}
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
