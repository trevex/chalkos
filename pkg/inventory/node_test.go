package inventory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// cluster is an inventory of a control plane and a worker that share cluster/ and run the image
// node-metal, the directory images/node-metal.
const cluster = `apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    node-metal: {source: {directory: images/node-metal}}
  image: node-metal
  files: [cluster/*.yaml]
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, files: [nodes/w1.yaml]}
`

const clusterFiles = `apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: homelab}
spec:
  endpoint: https://10.0.0.11:6443
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: monitoring}
spec:
  objects: []
`

// nodeFile is a node's file: its Node of the type, its Host of a hostname in .lab, and its uplink
// at the address.
func nodeFile(name, nodeType, address string) string {
	return "apiVersion: chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: " + name + "}\nspec: {type: " + nodeType + "}\n---\n" +
		"apiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {hostname: " + name + ".lab}\n---\n" +
		"apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {network: {address: [" + address + "/24]}}\n"
}

// homelabDir writes the cluster's inventory and files into a new directory and returns it.
func homelabDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), cluster)
	write(t, filepath.Join(dir, "cluster", "all.yaml"), clusterFiles)
	write(t, filepath.Join(dir, "nodes", "cp1.yaml"), nodeFile("cp1", "controlplane", "10.0.0.11"))
	write(t, filepath.Join(dir, "nodes", "w1.yaml"), nodeFile("w1", "worker", "10.0.0.21"))
	return dir
}

func kinds(docs []config.Document) []string {
	var out []string
	for _, d := range docs {
		out = append(out, d.Kind+"/"+d.Name)
	}
	return out
}

// TestNode gives a node the documents of its files in order, without the Addon on a worker, and
// reads its type, its Host's hostname, static addresses and the image the inventory names for it.
func TestNode(t *testing.T) {
	dir := homelabDir(t)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := inv.Node("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(cp1.Documents); !slices.Equal(got, []string{"Cluster/homelab", "Addon/monitoring", "Node/cp1", "Host/default", "Network/10-uplink"}) {
		t.Errorf("documents of cp1 = %q", got)
	}
	if cp1.Type != "controlplane" || cp1.Hostname != "cp1.lab" || cp1.ImageName != "node-metal" || cp1.ImageDir != filepath.Join(dir, "images", "node-metal") {
		t.Errorf("cp1 = %+v", cp1)
	}
	if got := cp1.StaticAddresses(); !slices.Equal(got, []string{"10.0.0.11"}) {
		t.Errorf("static addresses = %q", got)
	}
	w1, err := inv.Node("w1")
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(w1.Documents); !slices.Equal(got, []string{"Cluster/homelab", "Node/w1", "Host/default", "Network/10-uplink"}) {
		t.Errorf("documents of the worker w1 = %q, want no Addon", got)
	}
	// A Host whose hostname is null, and no Host, leave the hostname empty for the node's name.
	const host = "apiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {hostname: w1.lab}\n---\n"
	for name, file := range map[string]string{
		"a Host without a hostname": strings.Replace(nodeFile("w1", "worker", "10.0.0.21"), "{hostname: w1.lab}", "{hostname: null}", 1),
		"no Host":                   strings.Replace(nodeFile("w1", "worker", "10.0.0.21"), host, "", 1),
	} {
		write(t, filepath.Join(dir, "nodes", "w1.yaml"), file)
		if inv, err = Load(filepath.Join(dir, "chalkos.yaml")); err != nil {
			t.Fatal(err)
		}
		if w1, err = inv.Node("w1"); err != nil || w1.Hostname != "" {
			t.Errorf("%s: hostname = %q, %v, want none", name, w1.Hostname, err)
		}
	}
}

// TestNodeRefuses refuses a node whose files hold a document twice, no Node, or a Node of another
// name.
func TestNodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		file, content, want string
	}{
		"a document twice": {"nodes/cp1.yaml", nodeFile("cp1", "controlplane", "10.0.0.11") + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: homelab}\nspec: {}\n",
			`cluster/all.yaml and nodes/cp1.yaml both hold Cluster "homelab"`},
		"no Node":                {"nodes/cp1.yaml", "apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {}\n", "its files hold no Node document"},
		"a Node of another name": {"nodes/cp1.yaml", nodeFile("cp2", "controlplane", "10.0.0.11"), `its files hold the Node "cp2"`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := homelabDir(t)
			write(t, filepath.Join(dir, tc.file), tc.content)
			inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inv.Node("cp1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Node = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestResolve reads the metadata.json of the node's image directory, which names the image's
// platform, variant and root hash, and leaves the node's documents as its files give them: the
// root hash stays on the operator's machine.
func TestResolve(t *testing.T) {
	dir := homelabDir(t)
	hash := strings.Repeat("ab", 32)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := inv.Node("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cp1.Resolve(); err == nil || !strings.Contains(err.Error(), "the image node-metal of node cp1") ||
		!strings.Contains(err.Error(), "has no metadata.json; build the image or link its directory there") {
		t.Errorf("an image directory without metadata.json: %v", err)
	}
	write(t, filepath.Join(dir, "images", "node-metal", "metadata.json"),
		`{"schemaVersion": 0, "id": "chalkos", "variant": "node", "platform": "metal", "rootHash": "`+hash+`", "kinds": []}`)
	md, err := cp1.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if md.Platform != "metal" || md.Variant != "node" || md.RootHash != hash || cp1.Image == nil || cp1.Image.RootHash != hash {
		t.Errorf("metadata = %+v", md)
	}
	for _, d := range cp1.Documents {
		if spec, _ := d.Object["spec"].(map[string]any); spec["rootHash"] != nil {
			t.Errorf("Resolve gave %s a root hash: %v", d, d.Object)
		}
	}
	write(t, filepath.Join(dir, "images", "node-metal", "metadata.json"),
		`{"schemaVersion": 0, "id": "chalkos", "variant": "node", "platform": "metal", "kinds": []}`)
	w1, err := inv.Node("w1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w1.Resolve(); err == nil || !strings.Contains(err.Error(), "names no root hash") {
		t.Errorf("metadata.json without a root hash: %v", err)
	}
}

// TestCluster names the cluster as every node's Cluster does, and refuses nodes that disagree.
func TestCluster(t *testing.T) {
	dir := homelabDir(t)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if name, endpoint, err := inv.Cluster(); err != nil || name != "homelab" || endpoint != "https://10.0.0.11:6443" {
		t.Errorf("cluster = %q, %q, %v", name, endpoint, err)
	}
	write(t, filepath.Join(dir, "nodes", "w1.yaml"), nodeFile("w1", "worker", "10.0.0.21")+"---\napiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: other}\nspec: {}\n")
	inv, err = Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inv.Cluster(); err == nil || !strings.Contains(err.Error(), "w1") {
		t.Errorf("two Clusters for w1: %v", err)
	}
	// An inventory without nodes takes its Cluster from the shared files.
	write(t, filepath.Join(dir, "chalkos.yaml"), "apiVersion: chalkctl.chalkos.dev/v1alpha1\nkind: Inventory\nmetadata: {name: homelab}\nspec:\n  files: [cluster/*.yaml]\n")
	inv, err = Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if name, _, err := inv.Cluster(); err != nil || name != "homelab" {
		t.Errorf("cluster without nodes = %q, %v", name, err)
	}
}

// TestNodesOwnTheirDocuments gives each node its own copy of the documents of a file they share.
func TestNodesOwnTheirDocuments(t *testing.T) {
	dir := homelabDir(t)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := inv.Node("cp1")
	if err != nil {
		t.Fatal(err)
	}
	cp1.Documents[0].Object["spec"].(map[string]any)["endpoint"] = "https://changed:6443"
	w1, err := inv.Node("w1")
	if err != nil {
		t.Fatal(err)
	}
	if got := w1.Documents[0].Object["spec"].(map[string]any)["endpoint"]; got != "https://10.0.0.11:6443" {
		t.Errorf("w1's Cluster endpoint = %v after a change to cp1's", got)
	}
}

// TestResolveRefusesMetadata refuses metadata.json whose root hash is not one, or that names no
// platform.
func TestResolveRefusesMetadata(t *testing.T) {
	for name, tc := range map[string]struct{ hash, platform, want string }{
		"a short root hash":       {"abcd", "metal", `names the root hash "abcd", which is not 64 lower-case hex digits`},
		"an upper-case root hash": {strings.Repeat("AB", 32), "metal", "which is not 64 lower-case hex digits"},
		"no platform":             {strings.Repeat("ab", 32), "", "names no platform"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := homelabDir(t)
			write(t, filepath.Join(dir, "images", "node-metal", "metadata.json"),
				`{"schemaVersion": 0, "id": "chalkos", "variant": "node", "platform": "`+tc.platform+`", "rootHash": "`+tc.hash+`", "kinds": []}`)
			inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cp1, err := inv.Node("cp1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cp1.Resolve(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Resolve = %v, want %q", err, tc.want)
			}
		})
	}
}
