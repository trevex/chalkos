package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/schema"
)

// homelabManifest is the golden manifest of the homelab example.
func homelabManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	f, err := os.Open("../../test/fixtures/homelab-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := manifest.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture schemas lack the homelab image's Rack kind; the round trip over built images
	// covers its documents.
	for name, n := range m.Nodes {
		n.Documents = nil
		m.Nodes[name] = n
	}
	return m
}

// homelabTarget is the image of a homelab node, as far as the fixture schemas describe it.
func homelabTarget(m *manifest.Manifest, n manifest.Node) Target {
	return Target{Role: n.Role, Platform: n.Platform, Kubernetes: &kubernetes.Cluster{Kind: m.Roles[n.Role].Kind}}
}

func TestFromManifestGivesBackTheHomelab(t *testing.T) {
	m := homelabManifest(t)
	for name, n := range m.Nodes {
		t.Run(name, func(t *testing.T) {
			b, err := FromManifest(m, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Check(n.Identity, allSchemas(t), homelabTarget(m, n)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFromManifestDocuments(t *testing.T) {
	b, err := FromManifest(homelabManifest(t), "w1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range b.Documents {
		got = append(got, d.APIVersion+" "+d.String())
	}
	want := []string{
		`chalkos.dev/v1alpha1 Cluster "homelab"`,
		`chalkos.dev/v1alpha1 Image "worker-metal"`,
		`chalkos.dev/v1alpha1 Node "w1"`,
		`time.chalkos.dev/v1alpha1 TimeSync "time"`,
		`storage.chalkos.dev/v1alpha1 SystemDisk "system"`,
		`storage.chalkos.dev/v1alpha1 Volume "longhorn"`,
		`kubernetes.chalkos.dev/v1alpha1 Kubelet "kubelet"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("documents =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	spec := func(kind string) map[string]any {
		for _, d := range b.Documents {
			if d.Kind == kind {
				return d.Object["spec"].(map[string]any)
			}
		}
		return nil
	}
	if got := spec("Image")["source"]; got != "roles.worker.images.metal" {
		t.Errorf("image source = %v", got)
	}
	wantVolume := map[string]any{
		"disk":       map[string]any{"model": "Samsung SSD 870*", "type": "ssd"},
		"format":     "xfs",
		"mountPoint": "/var/lib/longhorn",
		"encryption": map[string]any{"mode": "tpm2"},
	}
	if got := spec("Volume"); !reflect.DeepEqual(got, wantVolume) {
		t.Errorf("volume = %v, want %v", got, wantVolume)
	}
	if h, err := Hash(b.Documents); err != nil || h != b.Version {
		t.Errorf("version = %s, want the documents' hash %s (%v)", b.Version, h, err)
	}
	docs, err := Parse(strings.NewReader(b.Text))
	if err != nil || len(docs) != len(b.Documents) {
		t.Errorf("the text holds %d documents (%v), want %d", len(docs), err, len(b.Documents))
	}
}

func TestFromManifestRefusesUnknownNodes(t *testing.T) {
	if _, err := FromManifest(homelabManifest(t), "w9"); err == nil || !strings.Contains(err.Error(), "no node w9") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckNamesWhatDiffers(t *testing.T) {
	m := homelabManifest(t)
	n := m.Nodes["cp1"]
	b, err := FromManifest(m, "cp1")
	if err != nil {
		t.Fatal(err)
	}
	want := n.Identity
	want.Hostname = "other"
	want.NetworkUnits = map[string]string{"10-uplink.network": "[Match]\nName=enp2s0\n"}
	_, err = b.Check(want, allSchemas(t), homelabTarget(m, n))
	for _, s := range []string{"hostname", "network units"} {
		if err == nil || !strings.Contains(err.Error(), s) {
			t.Errorf("err = %v, want it to name the %s", err, s)
		}
	}
}

func TestPassthrough(t *testing.T) {
	got, err := passthrough("[Partition]\nEncrypt=tpm2\nFormat=xfs\nLabel=data\nPadding\\=x=1\nTPM2PCRs=7\nType=63dd1eb4-1dd2-0d0b-0c1e-27c204214ec8\nWeight=2000\n")
	if want := map[string]any{"Padding=x": "1", "Weight": "2000"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("passthrough = %v, %v; want %v", got, err, want)
	}
	// Without Encrypt=tpm2, TPM2PCRs is the volume's own key.
	got, err = passthrough("[Partition]\nLabel=data\nTPM2PCRs=1\n")
	if want := map[string]any{"TPM2PCRs": "1"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("passthrough = %v, %v; want %v", got, err, want)
	}
	if _, err := passthrough("[Other]\nx=1\n"); err == nil {
		t.Error("a definition of another section was read")
	}
}

func TestNetworkObjects(t *testing.T) {
	got, err := networkObjects(map[string]string{
		"10-uplink.network": "[Match]\nName=enp1s0\n\n[Network]\nAddress=10.0.0.11/24\nAddress=fd00::11/64\nGateway=10.0.0.1\n\n" +
			"[Route]\nDestination=10.1.0.0/16\nGateway=10.0.0.254\n\n[Route]\nDestination=10.2.0.0/16\nGateway=10.0.0.254\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{object(NetworkdAPI, "Network", "10-uplink", map[string]any{
		"match":   map[string]any{"name": "enp1s0"},
		"network": map[string]any{"address": []any{"10.0.0.11/24", "fd00::11/64"}, "gateway": "10.0.0.1"},
		"route": []any{
			map[string]any{"destination": "10.1.0.0/16", "gateway": "10.0.0.254"},
			map[string]any{"destination": "10.2.0.0/16", "gateway": "10.0.0.254"},
		},
	})}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("objects = %v, want %v", got, want)
	}
	if _, err := networkObjects(map[string]string{"10-x.service": "[Unit]\n"}); err == nil {
		t.Error("a unit of another type was read")
	}
	if _, err := networkObjects(map[string]string{"10-x.network": "Name=x\n"}); err == nil {
		t.Error("a key outside a section was read")
	}
}

func TestSameINI(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"[A]\nx=1\ny=2\n\n[B]\nz=3\n", "[B]\nz=3\n\n[A]\ny=2\nx=1\n", true},
		{"[A]\nx=1\n", "[A]\nx=2\n", false},
		{"[A]\nx=1\nx=1\n", "[A]\nx=1\n", false},
		{"[A]\nx=1\n", "[A]\nx=1\n[A]\n", false},
	} {
		if got := sameINI(c.a, c.b); got != c.same {
			t.Errorf("sameINI(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestStaticAddresses(t *testing.T) {
	docs := mustParse(t, `apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-b}
spec: {network: {address: [10.0.1.5/24]}}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-a}
spec: {network: {address: [10.0.0.11/24, fd00::11/64]}}
`)
	if got, want := StaticAddresses(docs), []string{"10.0.0.11", "fd00::11", "10.0.1.5"}; !slices.Equal(got, want) {
		t.Errorf("addresses = %v, want %v", got, want)
	}
}

// fixtureSchemas compiles the schemas of the fixture directories together.
func fixtureSchemas(t *testing.T, dirs ...string) *schema.Schemas {
	t.Helper()
	files := map[string][]byte{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = data
		}
	}
	s, err := schema.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFromManifestAppendsDocuments(t *testing.T) {
	m := homelabManifest(t)
	n := m.Nodes["cp1"]
	n.Documents = []json.RawMessage{
		json.RawMessage(`{"apiVersion": "demo.example.org/v1alpha1", "kind": "Demo", "metadata": {"name": "one"},
		  "spec": {"name": "first", "replicas": 3, "token": {"secretRef": {"name": "demo", "key": "token"}}}}`),
		json.RawMessage(`{"apiVersion": "chalkos.dev/v1alpha1", "kind": "Secret", "metadata": {"name": "demo"}, "spec": {"data": {"token": "s3cret"}}}`),
	}
	m.Nodes["cp1"] = n
	b, err := FromManifest(m, "cp1")
	if err != nil {
		t.Fatal(err)
	}
	s := fixtureSchemas(t, builtinSchemas, "../../test/fixtures/config/networkd/schemas", "../../test/fixtures/kinds/image/schemas")
	if _, err := b.Check(n.Identity, s, homelabTarget(m, n)); err != nil {
		t.Fatal(err)
	}
	last := b.Documents[len(b.Documents)-2:]
	if last[0].Kind != "Demo" || last[1].Kind != "Secret" {
		t.Fatalf("last documents = %s, %s; want the extension's", last[0], last[1])
	}
	if got := last[0].Object["spec"].(map[string]any)["replicas"]; got != json.Number("3") {
		t.Errorf("replicas = %#v, want the number as written", got)
	}
	n.Documents = []json.RawMessage{json.RawMessage(`[1]`)}
	m.Nodes["cp1"] = n
	if _, err := FromManifest(m, "cp1"); err == nil || !strings.Contains(err.Error(), "document 1 of its extensions") {
		t.Errorf("err = %v, want the document that is no object named", err)
	}
}
