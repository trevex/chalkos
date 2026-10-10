package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// labManifest is a cluster of a control plane and a worker on kvm, as the lab template defines
// them, and a node on metal.
const labManifest = `{
  "schemaVersion": 0,
  "cluster": {"name": "lab", "endpoint": "https://192.168.123.11:6443"},
  "roles": {"controlplane": {"images": {"kvm": "roles.controlplane.images.kvm", "metal": "roles.controlplane.images.metal"}, "kind": "controlplane"},
            "worker": {"images": {"kvm": "roles.worker.images.kvm", "metal": "roles.worker.images.metal"}, "kind": "worker"}},
  "nodes": {
    "cp1": {"role": "controlplane", "platform": "kvm", "identity": {"hostname": "cp1", "platform": "kvm",
      "network": {"networks": {"10-lab": {"matchConfig": {"MACAddress": "52:54:00:7b:00:11"}, "address": ["192.168.123.11/24"]}}},
      "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}},
    "w1": {"role": "worker", "platform": "kvm", "identity": {"hostname": "w1", "platform": "kvm",
      "network": {"networks": {"10-lab": {"matchConfig": {"MACAddress": "52:54:00:7B:00:12"}, "address": ["192.168.123.12/24"]}}},
      "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}},
    "m1": {"role": "worker", "platform": "metal", "identity": {"hostname": "m1", "platform": "metal",
      "network": {}, "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}}
  }
}`

func decodeManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Decode(strings.NewReader(labManifest))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testApp() (*app, *bytes.Buffer) {
	var out bytes.Buffer
	return &app{stdin: strings.NewReader(""), stdout: &out, stderr: io.Discard,
		output:   func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not in the test") },
		chalkctl: func(context.Context, io.Writer, ...string) error { return errors.New("not in the test") },
	}, &out
}

// TestLabNodes runs the kvm nodes named, each with the MAC address its lab network matches, and
// refuses nodes of another platform or without one MAC address.
func TestLabNodes(t *testing.T) {
	m := decodeManifest(t)
	nodes, err := labNodes(m, []string{"w1", "cp1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []lab.LabNode{
		{Name: "cp1", Role: "controlplane", Kind: "controlplane", MAC: "52:54:00:7b:00:11"},
		{Name: "w1", Role: "worker", Kind: "worker", MAC: "52:54:00:7b:00:12"},
	}
	if !slices.Equal(nodes, want) {
		t.Errorf("nodes = %+v, want %+v", nodes, want)
	}
	if _, err := labNodes(m, nil); err == nil || !strings.Contains(err.Error(), "m1 is declared on metal") {
		t.Errorf("a lab of every node = %v, want m1 refused", err)
	}
	for name, network := range map[string]map[string]any{
		"matches none":        {},
		"matches several":     {"networks": map[string]any{"a": map[string]any{"matchConfig": map[string]any{"MACAddress": "52:54:00:00:00:01"}}, "b": map[string]any{"matchConfig": map[string]any{"MACAddress": "52:54:00:00:00:02"}}}},
		"not one MAC address": {"networks": map[string]any{"a": map[string]any{"matchConfig": map[string]any{"MACAddress": "52:54:00:00:00:01 52:54:00:00:00:02"}}}},
	} {
		n := m.Nodes["w1"]
		n.Identity.Network = network
		m.Nodes["w1"] = n
		if _, err := labNodes(m, []string{"w1"}); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("a network that %s: %v", name, err)
		}
	}
	n := m.Nodes["w1"]
	n.Identity.Network = m.Nodes["cp1"].Identity.Network
	m.Nodes["w1"] = n
	if _, err := labNodes(m, []string{"cp1", "w1"}); err == nil || !strings.Contains(err.Error(), "the same MAC address") {
		t.Errorf("two nodes of one MAC address: %v", err)
	}
}

// TestCreateKeys writes the lab's Secure Boot keys for its owner alone, and enrolls their
// certificates in the firmware's variables with Secure Boot on.
func TestCreateKeys(t *testing.T) {
	a, _ := testApp()
	var enrolled []string
	a.output = func(_ context.Context, name string, args ...string) ([]byte, error) {
		enrolled = append([]string{name}, args...)
		return nil, nil
	}
	dir := filepath.Join(t.TempDir(), "keys")
	if err := a.createKeys(context.Background(), dir, "lab", "/fw/VARS.fd"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the keys' directory: %v, %v", info.Mode(), err)
	}
	for _, name := range []string{"PK", "KEK", "db"} {
		info, err := os.Stat(filepath.Join(dir, name+".key"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s.key: %v, %v; want mode 0600", name, info.Mode(), err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, name+".crt"))
		block, _ := pem.Decode(data)
		if block == nil {
			t.Fatalf("%s.crt holds no PEM", name)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || cert.Subject.CommonName != "chalklab lab "+name || cert.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
			t.Errorf("%s.crt: %v, %v", name, cert.Subject, err)
		}
	}
	args := strings.Join(enrolled, " ")
	for _, want := range []string{"virt-fw-vars --loglevel warning --input /fw/VARS.fd --output " + filepath.Join(dir, "OVMF_VARS.fd"), "--set-pk", "--add-kek", "--add-db", "--secure-boot"} {
		if !strings.Contains(args, want) {
			t.Errorf("enrolled with %q, want %q", args, want)
		}
	}
	if strings.Contains(args, ".key") {
		t.Errorf("a private key was named to virt-fw-vars: %q", args)
	}
	if err := a.createKeys(context.Background(), dir, "lab", "/fw/VARS.fd"); err == nil {
		t.Error("created keys over the lab's keys")
	}
}

// TestCreateLeavesNothingWhenItFails refuses a lab that exists, and removes what a failed create
// made before anything ran.
func TestCreateLeavesNothingWhenItFails(t *testing.T) {
	// Short, as the lab's socket paths must be.
	state, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CHALKLAB_OVMF_CODE", "/fw/CODE.fd")
	t.Setenv("CHALKLAB_OVMF_VARS", "/fw/VARS.fd")
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(labManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, _ := lab.StateDir("lab")
	a, _ := testApp()
	err = a.run(context.Background(), []string{"create", "--manifest", path, "--nodes", "cp1,w1", "--image", "controlplane=/nix/store/x"})
	if err == nil || !strings.Contains(err.Error(), "pass --image worker=DIR") {
		t.Fatalf("create without the worker's image = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed create left %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	err = a.run(context.Background(), []string{"create", "--manifest", path, "--nodes", "cp1,w1"})
	if err == nil || !strings.Contains(err.Error(), "chalklab destroy removes it") {
		t.Fatalf("create over a lab = %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("create removed the existing lab: %v", err)
	}
}

// TestForwardedClientFile names the lab's nodes at their forwarded ports in the client file and
// keeps it its owner's alone.
func TestForwardedClientFile(t *testing.T) {
	secrets, err := pki.GenerateSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.NewConfig(secrets.OSCA, secrets.OSCABundle(), "lab", "chalklab", pki.RoleAdmin, time.Hour, map[string]string{"cp1": "192.168.123.11", "m1": "10.0.0.5"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := c.Encode()
	path := filepath.Join(t.TempDir(), clientFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	l := &lab.Lab{Nodes: []lab.LabNode{{Name: "cp1", ChalkdPort: 15001}, {Name: "w1", ChalkdPort: 15002}}}
	if err := forwardedClientFile(path, l); err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PreferNodeAddresses {
		t.Error("the client file does not prefer its addresses to the cluster definition's")
	}
	if len(got.Nodes) != 2 || got.Nodes["cp1"] != "127.0.0.1:15001" || got.Nodes["w1"] != "127.0.0.1:15002" {
		t.Errorf("nodes = %v", got.Nodes)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the client file: %v, %v; want mode 0600", info.Mode(), err)
	}
}

// TestLabDir takes the only lab there is, and asks to choose among several.
func TestLabDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, _, err := labDir(""); err == nil || !strings.Contains(err.Error(), "chalklab create starts one") {
		t.Errorf("labDir without labs = %v", err)
	}
	for _, name := range []string{"lab", "other"} {
		dir, _ := lab.StateDir(name)
		os.MkdirAll(dir, 0o700)
		if err := (&lab.Lab{Cluster: name}).Write(dir); err != nil {
			t.Fatal(err)
		}
		if name == "lab" {
			if dir, l, err := labDir(""); err != nil || l.Cluster != "lab" || !strings.HasSuffix(dir, "/chalklab/lab") {
				t.Errorf("labDir with one lab = %q, %v, %v", dir, l, err)
			}
		}
	}
	if _, _, err := labDir(""); err == nil || !strings.Contains(err.Error(), "choose one with --cluster") {
		t.Errorf("labDir with two labs = %v", err)
	}
	if _, l, err := labDir("other"); err != nil || l.Cluster != "other" {
		t.Errorf("labDir(other) = %v, %v", l, err)
	}
	if _, _, err := labDir("none"); err == nil {
		t.Error("found a lab of a cluster without one")
	}
}
