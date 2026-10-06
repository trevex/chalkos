package manifest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"schemaVersion": 1}`))
	if err == nil || !strings.Contains(err.Error(), "older than the cluster definition") {
		t.Fatalf("err = %v, want unsupported version error", err)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"schemaVersion": 0, "surprise": true}`))
	if err == nil {
		t.Fatal("unknown field accepted")
	}
}
