package inventory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// cluster is an inventory of a control plane and a worker that share cluster/ and run the Image
// node-metal, the directory images/node-metal.
const cluster = `apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
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
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: node-metal}
spec:
  source: {directory: ../images/node-metal}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: monitoring}
spec:
  objects: []
`

// nodeFile is a node's file: its Node of the type and hostname, and its uplink at the address.
func nodeFile(name, nodeType, address string) string {
	return "apiVersion: chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: " + name + "}\nspec: {type: " + nodeType + ", imageRef: node-metal, hostname: " + name + ".lab}\n---\n" +
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
// reads its type, hostname, static addresses and image directory, relative to the Image's file.
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
	if got := kinds(cp1.Documents); !slices.Equal(got, []string{"Cluster/homelab", "Image/node-metal", "Addon/monitoring", "Node/cp1", "Network/10-uplink"}) {
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
	if got := kinds(w1.Documents); !slices.Equal(got, []string{"Cluster/homelab", "Image/node-metal", "Node/w1", "Network/10-uplink"}) {
		t.Errorf("documents of the worker w1 = %q, want no Addon", got)
	}
}

// TestNodeRefuses refuses a node whose files hold a document twice, no Node, a Node of another
// name, or no Image of the Node's imageRef, and an Image without a directory.
func TestNodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		file, content, want string
	}{
		"a document twice": {"nodes/cp1.yaml", nodeFile("cp1", "controlplane", "10.0.0.11") + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: homelab}\nspec: {}\n",
			`cluster/all.yaml and nodes/cp1.yaml both hold Cluster "homelab"`},
		"no Node":                      {"nodes/cp1.yaml", "apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {}\n", "its files hold no Node document"},
		"a Node of another name":       {"nodes/cp1.yaml", nodeFile("cp2", "controlplane", "10.0.0.11"), `its files hold the Node "cp2"`},
		"no such Image":                {"nodes/cp1.yaml", strings.Replace(nodeFile("cp1", "controlplane", "10.0.0.11"), "imageRef: node-metal", "imageRef: node-kvm", 1), `runs the Image "node-kvm", which its files do not hold`},
		"an Image without a directory": {"cluster/all.yaml", strings.Replace(clusterFiles, "source: {directory: ../images/node-metal}", "source: {}", 1), `its Image "node-metal" names no source directory`},
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
	if _, err := cp1.Resolve(); err == nil || !strings.Contains(err.Error(), `the Image "node-metal" of node cp1`) {
		t.Errorf("an image directory without metadata.json: %v", err)
	}
	write(t, filepath.Join(dir, "images", "node-metal", "metadata.json"),
		`{"schemaVersion": 0, "id": "chalkos", "version": "0.3.0", "variant": "node", "platform": "metal", "rootHash": "`+hash+`", "kinds": []}`)
	md, err := cp1.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if md.Platform != "metal" || md.Variant != "node" || md.RootHash != hash || cp1.Image == nil || cp1.Image.RootHash != hash {
		t.Errorf("metadata = %+v", md)
	}
	for _, d := range cp1.Documents {
		if spec, _ := d.Object["spec"].(map[string]any); d.Kind == "Image" && spec["rootHash"] != nil {
			t.Errorf("Resolve gave the Image a root hash: %v", d.Object)
		}
	}
	write(t, filepath.Join(dir, "images", "node-metal", "metadata.json"),
		`{"schemaVersion": 0, "id": "chalkos", "version": "0.3.0", "variant": "node", "platform": "metal", "kinds": []}`)
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

// TestNodeImageDirectory resolves an Image's relative source against the directory of the file
// that holds it, as the inventory names the file, and takes an absolute source as written.
func TestNodeImageDirectory(t *testing.T) {
	images := t.TempDir()
	for name, tc := range map[string]struct{ source, want string }{
		"absolute":        {filepath.Join(images, "node-metal"), filepath.Join(images, "node-metal")},
		"up and down":     {"../images/../builds/./node-metal", "builds/node-metal"},
		"beside the file": {"node-metal", "cluster/node-metal"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := homelabDir(t)
			write(t, filepath.Join(dir, "cluster", "all.yaml"), strings.Replace(clusterFiles, "../images/node-metal", tc.source, 1))
			inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cp1, err := inv.Node("cp1")
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if !filepath.IsAbs(want) {
				want = filepath.Join(dir, want)
			}
			if cp1.ImageDir != want {
				t.Errorf("image directory = %s, want %s", cp1.ImageDir, want)
			}
		})
	}
	// A shared file linked into the cluster's directory points at the images beside the link.
	dir := homelabDir(t)
	shared := filepath.Join(t.TempDir(), "all.yaml")
	write(t, shared, clusterFiles)
	if err := os.Remove(filepath.Join(dir, "cluster", "all.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dir, "cluster", "all.yaml")); err != nil {
		t.Fatal(err)
	}
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := inv.Node("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "images", "node-metal"); cp1.ImageDir != want {
		t.Errorf("image directory through a symlink = %s, want %s", cp1.ImageDir, want)
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
				`{"schemaVersion": 0, "id": "chalkos", "version": "0.3.0", "variant": "node", "platform": "`+tc.platform+`", "rootHash": "`+tc.hash+`", "kinds": []}`)
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
