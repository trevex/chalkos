package manifest

import (
	"encoding/json"
	"os"
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
	if m.Cluster.Name != "homelab" || m.Cluster.Endpoint != "https://10.0.0.10:6443" {
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
	if st.Volumes["var"].Size != "200G" || st.Fallback != "recovery-key" {
		t.Errorf("w1 storage = %+v", st)
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
