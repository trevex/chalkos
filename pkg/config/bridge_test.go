package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/networkdschema"
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

// homelabTarget is the image of a homelab node: the fixture schemas, and on a node with
// Kubernetes the facts of a flannel image.
func homelabTarget(t *testing.T, m *manifest.Manifest, n manifest.Node) Target {
	t.Helper()
	target := Target{Role: n.Role, Platform: n.Platform}
	if m.Roles[n.Role].Kind != "" {
		img := testImage
		target.Image = &img
	}
	return target
}

func TestFromManifestGivesBackTheHomelab(t *testing.T) {
	m := homelabManifest(t)
	for name, n := range m.Nodes {
		t.Run(name, func(t *testing.T) {
			b, err := FromManifest(m, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Check(n.Identity, allSchemas(t), homelabTarget(t, m, n)); err != nil {
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
		`kubernetes.chalkos.dev/v1alpha1 Kubernetes "kubernetes"`,
		`kubernetes.chalkos.dev/v1alpha1 Flannel "flannel"`,
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
	_, err = b.Check(want, allSchemas(t), homelabTarget(t, m, n))
	for _, s := range []string{"hostname", "network unit 10-uplink.network: [Match] Name=", "network unit 10-uplink.network: 1 [Network] sections instead of 0"} {
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

func TestFromManifestRefusesRepeatedRepartKeys(t *testing.T) {
	m := homelabManifest(t)
	n := m.Nodes["w1"]
	d := n.Identity.Storage.Disks["longhorn"]
	d.Repart = map[string]string{"10-longhorn.conf": d.Repart["10-longhorn.conf"] + "CopyFiles=/a\nCopyFiles=/b\n"}
	n.Identity.Storage.Disks = maps.Clone(n.Identity.Storage.Disks)
	n.Identity.Storage.Disks["longhorn"] = d
	m.Nodes["w1"] = n
	_, err := FromManifest(m, "w1")
	if want := "volume longhorn: repart key CopyFiles has several values; a Volume takes one value per key"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
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
		// The order of a key's values is kept: networkd tries DNS servers in order.
		{"[Network]\nDNS=10.0.0.1\nDNS=10.0.0.2\n", "[Network]\nDNS=10.0.0.2\nDNS=10.0.0.1\n", false},
		{"[Network]\nDNS=10.0.0.1\nAddress=10.0.0.5/24\nDNS=10.0.0.2\n", "[Network]\nAddress=10.0.0.5/24\nDNS=10.0.0.1\nDNS=10.0.0.2\n", true},
		// Repeated sections keep their order among themselves, not among other sections.
		{"[Route]\nDestination=10.1.0.0/16\n\n[Route]\nDestination=10.2.0.0/16\n", "[Route]\nDestination=10.2.0.0/16\n\n[Route]\nDestination=10.1.0.0/16\n", false},
		{"[Route]\nDestination=10.1.0.0/16\n\n[Network]\nDNS=10.0.0.1\n\n[Route]\nDestination=10.2.0.0/16\n",
			"[Network]\nDNS=10.0.0.1\n\n[Route]\nDestination=10.1.0.0/16\n\n[Route]\nDestination=10.2.0.0/16\n", true},
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
	if _, err := b.Check(n.Identity, s, homelabTarget(t, m, n)); err != nil {
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
	n.Documents = []json.RawMessage{json.RawMessage(`{"apiVersion": "chalkos.dev/v1alpha1", "kind": "Secret", "metadata": {"name": "demo"}, "spec": {"data": {}}}`), json.RawMessage(`null`)}
	m.Nodes["cp1"] = n
	if _, err := FromManifest(m, "cp1"); err == nil || !strings.Contains(err.Error(), "node cp1: document 2 of its extensions") || !strings.Contains(err.Error(), "null") {
		t.Errorf("err = %v, want the null document refused by node and index", err)
	}
}

// TestCheckRefusesExtensionsThatCollide refuses extension documents that repeat a kind the image
// accepts once, such as a second Node or Rack, and says they came from the node's documents.
func TestCheckRefusesExtensionsThatCollide(t *testing.T) {
	const rack = `{"apiVersion": "rack.example.org/v1alpha1", "kind": "Rack", "metadata": {"name": "rack"}, "spec": {"location": "rack-a/u12"}}`
	dir := t.TempDir()
	// The Rack kind as the homelab's role images declare it: one per node.
	if err := os.WriteFile(filepath.Join(dir, "rack.example.org_v1alpha1_Rack.json"), []byte(`{
	  "$schema": "https://json-schema.org/draft/2020-12/schema", "title": "Rack", "type": "object",
	  "properties": {
	    "apiVersion": {"const": "rack.example.org/v1alpha1"}, "kind": {"const": "Rack"},
	    "metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"], "additionalProperties": false},
	    "spec": {"type": "object", "properties": {"location": {"type": "string", "minLength": 1}}, "required": ["location"], "additionalProperties": false}
	  },
	  "required": ["apiVersion", "kind", "metadata", "spec"], "additionalProperties": false,
	  "x-chalkos-kind": {"group": "rack.example.org", "kind": "Rack", "multiple": false, "version": "v1alpha1"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := fixtureSchemas(t, builtinSchemas, "../../test/fixtures/config/networkd/schemas", dir)
	for name, tc := range map[string]struct {
		docs []string
		want string
	}{
		"a second Node": {
			docs: []string{`{"apiVersion": "chalkos.dev/v1alpha1", "kind": "Node", "metadata": {"name": "other"}, "spec": {"imageRef": "controlplane-metal"}}`},
			want: "more than one Node",
		},
		"a second Rack of the same name": {
			docs: []string{rack, rack},
			want: "more than one Rack",
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := homelabManifest(t)
			n := m.Nodes["cp1"]
			for _, d := range tc.docs {
				n.Documents = append(n.Documents, json.RawMessage(d))
			}
			m.Nodes["cp1"] = n
			b, err := FromManifest(m, "cp1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = b.Check(n.Identity, s, homelabTarget(t, m, n))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "chalkos.nodes.cp1.documents") {
				t.Errorf("err = %v, want %q from chalkos.nodes.cp1.documents", err, tc.want)
			}
		})
	}
	// One Rack is accepted; a generated document that breaks the set is not blamed on them.
	m := homelabManifest(t)
	n := m.Nodes["cp1"]
	n.Documents = []json.RawMessage{json.RawMessage(rack)}
	m.Nodes["cp1"] = n
	b, err := FromManifest(m, "cp1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Check(n.Identity, s, homelabTarget(t, m, n)); err != nil {
		t.Fatal(err)
	}
	b.Documents = slices.DeleteFunc(slices.Clone(b.Documents), func(d Document) bool { return d.Kind == "SystemDisk" })
	if _, err := b.Check(n.Identity, s, homelabTarget(t, m, n)); err == nil || strings.Contains(err.Error(), "documents") {
		t.Errorf("err = %v, want the missing SystemDisk not blamed on the node's documents", err)
	}
}

// TestBridgeGivesBackEveryManifest generates the config of every node of every manifest the
// repository has, which CHALKOS_TEST_BRIDGE holds with the contract of each image their nodes run,
// and checks that each gives back the node's identity on that image.
func TestBridgeGivesBackEveryManifest(t *testing.T) {
	root := os.Getenv("CHALKOS_TEST_BRIDGE")
	if root == "" {
		t.Skip("CHALKOS_TEST_BRIDGE is not set; the go-unit check sets it")
	}
	sets, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	nodes := 0
	for _, set := range sets {
		dir := filepath.Join(root, set.Name())
		f, err := os.Open(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := manifest.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", set.Name(), err)
		}
		names := make([]string, 0, len(m.Nodes))
		for name := range m.Nodes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			n := m.Nodes[name]
			nodes++
			t.Run(set.Name()+"/"+name, func(t *testing.T) {
				contract := filepath.Join(dir, "contracts", n.Role+"-"+n.Platform)
				s, err := schema.LoadSchemas(filepath.Join(contract, "schemas"))
				if err != nil {
					t.Fatal(err)
				}
				target := Target{Role: n.Role, Platform: n.Platform}
				data, err := os.ReadFile(filepath.Join(contract, "image.json"))
				switch {
				case err == nil:
					img, err := kubernetes.ParseImage(data)
					if err != nil {
						t.Fatal(err)
					}
					target.Image = &img
				case !errors.Is(err, fs.ErrNotExist):
					t.Fatal(err)
				}
				b, err := FromManifest(m, name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := b.Check(n.Identity, s, target); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	if nodes == 0 {
		t.Fatalf("%s holds no node", root)
	}
}

// cloneIdentity copies an identity deeply, so a test changes its copy alone.
func cloneIdentity(t *testing.T, id manifest.Identity) manifest.Identity {
	t.Helper()
	b, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	var out manifest.Identity
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCheckNamesEachField(t *testing.T) {
	m := homelabManifest(t)
	for name, c := range map[string]struct {
		node   string
		change func(id *manifest.Identity)
		want   string
	}{
		"volume encryption": {"w1", func(id *manifest.Identity) {
			v := id.Storage.Volumes["longhorn"]
			v.Encryption = "none"
			id.Storage.Volumes["longhorn"] = v
		}, "volume longhorn: encryption tpm2 instead of none"},
		"seed": {"w1", func(id *manifest.Identity) {
			d := id.Storage.Disks["longhorn"]
			d.Seed = "00000000-0000-0000-0000-000000000000"
			id.Storage.Disks["longhorn"] = d
		}, "disk longhorn: seed 7dc5cf32-67a9-6501-acce-377a47f9df6c instead of 00000000-0000-0000-0000-000000000000"},
		"repart": {"w1", func(id *manifest.Identity) {
			d := id.Storage.Disks["longhorn"]
			d.Repart["10-longhorn.conf"] += "Weight=1000\n"
			id.Storage.Disks["longhorn"] = d
		}, "disk longhorn: 10-longhorn.conf: [Partition] Weight= (none) instead of 1000"},
		"validSubnets": {"cp1", func(id *manifest.Identity) {
			id.Kubernetes.ValidSubnets = []string{}
		}, "kubernetes validSubnets null instead of []"},
		"nodeIPs": {"cp1", func(id *manifest.Identity) {
			id.Kubernetes.NodeIPs = []string{"10.0.0.12"}
		}, "kubernetes nodeIPs [10.0.0.11] instead of [10.0.0.12]"},
		"time server port": {"cp1", func(id *manifest.Identity) {
			id.Time.Servers[0].Port = 123
		}, "time server 1 (ptbtime1.ptb.de): port 0 instead of 123"},
		"time server nts": {"cp1", func(id *manifest.Identity) {
			id.Time.Servers[1].NTS = false
		}, "time server 2 (ptbtime2.ptb.de): nts true instead of false"},
		"MAC": {"cp1", func(id *manifest.Identity) {
			n := id.Network["networks"].(map[string]any)["10-uplink"].(map[string]any)
			n["matchConfig"] = map[string]any{"MACAddress": "52:54:00:12:34:56"}
		}, "network 10-uplink: matched MAC address (none) instead of 52:54:00:12:34:56"},
		"static addresses": {"cp1", func(id *manifest.Identity) {
			n := id.Network["networks"].(map[string]any)["10-uplink"].(map[string]any)
			n["address"] = []any{"10.0.0.12/24"}
		}, "static addresses [10.0.0.11] instead of [10.0.0.12]"},
		"labels": {"w1", func(id *manifest.Identity) {
			id.Labels["node.kubernetes.io/storage"] = "hdd"
		}, `label node.kubernetes.io/storage: "ssd" instead of "hdd"`},
		"taints": {"w1", func(id *manifest.Identity) {
			id.Taints[0].Effect = "NoExecute"
		}, "taints"},
	} {
		t.Run(name, func(t *testing.T) {
			n := m.Nodes[c.node]
			b, err := FromManifest(m, c.node)
			if err != nil {
				t.Fatal(err)
			}
			want := cloneIdentity(t, n.Identity)
			c.change(&want)
			_, err = b.Check(want, allSchemas(t), homelabTarget(t, m, n))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to say %q", err, c.want)
			}
			if strings.Contains(err.Error(), "; ") {
				t.Errorf("err = %v, want one difference", err)
			}
		})
	}
}

// bridgeSchemas are the built-in kinds' schemas and the networkd kinds', generated from a few
// sections and keys of each.
func bridgeSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	files := map[string][]byte{}
	entries, err := os.ReadDir(builtinSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(builtinSchemas, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = data
	}
	template, err := os.ReadFile("../../test/fixtures/config/networkd/schemas/networkd.chalkos.dev_v1alpha1_Network.json")
	if err != nil {
		t.Fatal(err)
	}
	for kind, keys := range map[string][]string{
		"Network": {"Match.Name", "Match.MACAddress", "Network.Address", "Network.DNS", "Network.Gateway", "Network.VLAN", "Route.Destination", "Route.Gateway"},
		"NetDev":  {"Match.Host", "NetDev.Name", "NetDev.Kind", "VLAN.Id"},
		"Link":    {"Match.MACAddress", "Link.Name"},
	} {
		var entries []networkdschema.Entry
		for _, k := range keys {
			section, key, _ := strings.Cut(k, ".")
			entries = append(entries, networkdschema.Entry{Section: section, Key: key})
		}
		spec, err := networkdschema.SpecSchema(entries)
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(template, &s); err != nil {
			t.Fatal(err)
		}
		s["title"] = kind
		s["properties"].(map[string]any)["kind"] = map[string]any{"const": kind}
		s["properties"].(map[string]any)["spec"] = spec["spec"]
		s["$defs"] = spec["$defs"]
		s["x-chalkos-kind"].(map[string]any)["kind"] = kind
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		files["networkd.chalkos.dev_v1alpha1_"+kind+".json"] = data
	}
	s, err := schema.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// bridgeBase is a node on an image without Kubernetes, which each round trip adds documents to.
const bridgeBase = `apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec: {}
---
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: img}
spec: {source: /images/plain}
---
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: n1}
spec: {imageRef: img}
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec: {disk: {path: /dev/vda}, encryption: {mode: tpm2, fallback: recovery-key}, var: {size: 10G}}
`

func TestFromManifestRoundTrips(t *testing.T) {
	s := bridgeSchemas(t)
	target := Target{Role: "plain", Platform: "kvm"}
	for name, extra := range map[string]string{
		"raw volume": `apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: raw}
spec: {format: null, size: 1G}
`,
		"encryption override": `apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: plain}
spec: {size: 2G, mountPoint: /srv, encryption: {mode: none}}
`,
		"repart passthrough": `apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: data}
spec: {disk: {serial: ABC123}, format: xfs, repart: {Weight: "2000", "Padding=x": "1", CopyBlocks: auto}}
`,
		"netdev and link": `apiVersion: networkd.chalkos.dev/v1alpha1
kind: Link
metadata: {name: 10-lan}
spec: {match: {macAddress: "52:54:00:12:34:56"}, link: {name: lan0}}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: NetDev
metadata: {name: 20-vlan10}
spec: {netDev: {name: vlan10, kind: vlan}, vlan: {id: 10}}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lan}
spec: {match: {macAddress: "52:54:00:12:34:56"}, network: {vlan: vlan10, dns: [10.0.0.2, 10.0.0.1]}}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-vlan10}
spec: {match: {name: vlan10}, network: {address: 10.0.10.5/24}}
`,
		"repeated routes": `apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {name: enp1s0}
  network: {address: [10.0.0.5/24, fd00::5/64], gateway: 10.0.0.1}
  route:
    - {destination: 10.2.0.0/16, gateway: 10.0.0.254}
    - {destination: 10.1.0.0/16, gateway: 10.0.0.253}
`,
		"time server with a port": `apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: time}
spec: {servers: [{host: ntp.example.org, nts: false, port: 4460}, {host: nts.example.org, nts: true}]}
`,
	} {
		t.Run(name, func(t *testing.T) {
			set, err := NewSet(mustParse(t, bridgeBase+"---\n"+extra), s)
			if err != nil {
				t.Fatal(err)
			}
			id, err := set.Identity(target)
			if err != nil {
				t.Fatal(err)
			}
			m := &manifest.Manifest{
				Cluster: manifest.Cluster{Name: "lab"},
				Roles:   map[string]manifest.Role{"plain": {Images: map[string]string{"kvm": "roles.plain.images.kvm"}}},
				Nodes:   map[string]manifest.Node{"n1": {Role: "plain", Platform: "kvm", Identity: id}},
			}
			b, err := FromManifest(m, "n1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Check(id, s, target); err != nil {
				t.Fatalf("%v\ndocuments:\n%s", err, b.Text)
			}
		})
	}
}

// TestFromManifestKubernetesDocuments: registries become RegistryMirrors named after them, the
// cluster's manifests one Manifest, and every TimeSync carries the DHCP flag.
func TestFromManifestKubernetesDocuments(t *testing.T) {
	m := homelabManifest(t)
	m.Registries = manifest.Registries{Mirrors: map[string][]string{
		"docker.io":      {"https://mirror.example"},
		"localhost:5000": {"http://10.0.2.100:5000"},
		// An IPv6 registry and mirror, and a mirror without a scheme, which containerd takes as
		// https.
		"[fd00::1]:5000": {"http://[fd00::2]:5000", "mirror.lan:5000"},
	}, AllowPlainHTTP: true}
	m.Manifests = []json.RawMessage{json.RawMessage(`{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "apps"}}`)}
	m.Time.DHCPServers = true
	// Only control planes apply the cluster's manifests, so workers do not get them.
	for node, manifests := range map[string][]string{"w1": nil, "cp1": {"manifests"}} {
		b, err := FromManifest(m, node)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string][]string{}
		for _, d := range b.Documents {
			names[d.Kind] = append(names[d.Kind], d.Name)
		}
		if !slices.Equal(names["RegistryMirror"], []string{"fd00--1-5000", "docker.io", "localhost-5000"}) || !slices.Equal(names["Manifest"], manifests) {
			t.Errorf("%s: documents = %v", node, names)
		}
		if _, err := b.Check(m.Nodes[node].Identity, allSchemas(t), homelabTarget(t, m, m.Nodes[node])); err != nil {
			t.Fatalf("%s: %v", node, err)
		}
	}
	// Two registries that would share a document name.
	m.Registries.Mirrors["localhost-5000"] = []string{"https://other.example"}
	if _, err := FromManifest(m, "w1"); err == nil || !strings.Contains(err.Error(), `both be the RegistryMirror "localhost-5000"`) {
		t.Errorf("err = %v", err)
	}
}

// TestFromManifestDropsUnusedFamilies: the cluster options check the ranges of the families in
// use alone, so a single-stack manifest may hold anything for the other family; its documents
// leave those values to the kind's defaults.
func TestFromManifestDropsUnusedFamilies(t *testing.T) {
	m := homelabManifest(t)
	m.Kubernetes = json.RawMessage(`{"ipFamilies": ["ipv4"],
		"podCIDRs": {"ipv4": "10.244.0.0/16", "ipv6": ""}, "serviceCIDRs": {"ipv4": "10.96.0.0/12", "ipv6": ""},
		"dnsIPs": {"ipv4": "10.96.0.10", "ipv6": ""}, "nodeCIDRMaskSizes": {"ipv4": 24, "ipv6": 0}}`)
	b, err := FromManifest(m, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Check(m.Nodes["w1"].Identity, allSchemas(t), homelabTarget(t, m, m.Nodes["w1"])); err != nil {
		t.Fatal(err)
	}
}

// TestCheckRefusesKubernetesSettingsTheNodeRefuses: the manifest's Kubernetes settings reach the
// Kubernetes document as they are, so settings chalkd refuses fail the check, naming the
// document.
func TestCheckRefusesKubernetesSettingsTheNodeRefuses(t *testing.T) {
	m := homelabManifest(t)
	m.Kubernetes = json.RawMessage(`{"podCIDRs": {"ipv4": "10.244.0.1/16"}}`)
	b, err := FromManifest(m, "w1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Check(m.Nodes["w1"].Identity, allSchemas(t), homelabTarget(t, m, m.Nodes["w1"]))
	if err == nil || !strings.Contains(err.Error(), `Kubernetes "kubernetes"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestCheckNamesTheDifferingManifestObject: a manifest object the documents do not give back is
// named by its place and its kind and name.
func TestCheckNamesTheDifferingManifestObject(t *testing.T) {
	m := homelabManifest(t)
	m.Manifests = []json.RawMessage{
		json.RawMessage(`{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "apps"}}`),
		json.RawMessage(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "big"}, "data": {}, "n": 9007199254740993}`),
	}
	b, err := FromManifest(m, "cp1")
	if err != nil {
		t.Fatal(err)
	}
	n := m.Nodes["cp1"]
	// Numbers beyond float64's precision survive the round trip.
	if _, err := b.Check(n.Identity, allSchemas(t), homelabTarget(t, m, n)); err != nil {
		t.Fatal(err)
	}
	b.manifests = slices.Clone(b.manifests)
	b.manifests[1] = json.RawMessage(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "big"}, "data": {}, "n": 9007199254740992}`)
	_, err = b.Check(n.Identity, allSchemas(t), homelabTarget(t, m, n))
	if err == nil || !strings.Contains(err.Error(), "manifest object 2 (ConfigMap big) differs from the cluster's") {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(err.Error(), "the config generated from the cluster definition does not give back the definition's ") {
		t.Errorf("err = %v", err)
	}
	b.manifests = b.manifests[:1]
	if _, err = b.Check(n.Identity, allSchemas(t), homelabTarget(t, m, n)); err == nil || !strings.Contains(err.Error(), "2 manifest objects instead of 1") {
		t.Fatalf("err = %v", err)
	}
}

// TestFromManifestCarriesExtraArgs: the components' extra flags, the kubelet's among them, reach
// the Kubernetes document as the manifest gives them.
func TestFromManifestCarriesExtraArgs(t *testing.T) {
	m := homelabManifest(t)
	m.Kubernetes = json.RawMessage(`{"extraArgs": {"kubelet": {"v": "2"}, "kube-apiserver": {"audit-log-maxage": "7"}}}`)
	for _, node := range []string{"w1", "cp1"} {
		n := m.Nodes[node]
		b, err := FromManifest(m, node)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Check(n.Identity, allSchemas(t), homelabTarget(t, m, n)); err != nil {
			t.Fatalf("%s: %v", node, err)
		}
		set, err := NewSet(b.Documents, allSchemas(t))
		if err != nil {
			t.Fatal(err)
		}
		c, err := set.Cluster(testImage)
		if err != nil {
			t.Fatal(err)
		}
		if c.ExtraArgs["kubelet"]["v"] != "2" || c.ExtraArgs["kube-apiserver"]["audit-log-maxage"] != "7" {
			t.Errorf("%s: extraArgs = %v", node, c.ExtraArgs)
		}
	}
}
