package chalkctl

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// testInventoryYAML is the inventory of the test cluster lab: n1 at 10.0.0.11, of cluster.yaml
// and nodes/n1.yaml.
const testInventoryYAML = `apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: lab}
spec:
  files: [cluster.yaml]
  nodes:
    n1: {address: 10.0.0.11, files: [nodes/n1.yaml]}
`

// testClusterYAML is the Cluster lab and the Image test-metal, the directory images/test-metal.
const testClusterYAML = `apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec:
  endpoint: https://10.0.0.10:6443
---
apiVersion: chalkos.dev/v1alpha1
kind: Image
metadata: {name: test-metal}
spec:
  source: {directory: images/test-metal}
`

// n1's documents: its Node, its system disk of serial chalk-target and its static address.
const (
	n1Node       = "apiVersion: chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: n1}\nspec: {imageRef: test-metal}\n"
	n1SystemDisk = "apiVersion: storage.chalkos.dev/v1alpha1\nkind: SystemDisk\nmetadata: {name: system}\nspec: {disk: {serial: chalk-target}}\n"
	n1Network    = "apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {network: {address: [10.0.0.11/24]}}\n"
)

// testRootHash is the root hash of the test image directory's metadata.json.
var testRootHash = strings.Repeat("ab", 32)

// writeInventory writes the test cluster's inventory into dir, with n1 of the documents given or
// else of n1Node, n1SystemDisk and n1Network, and returns the inventory's path.
func writeInventory(t *testing.T, dir string, docs ...string) string {
	t.Helper()
	if len(docs) == 0 {
		docs = []string{n1Node, n1SystemDisk, n1Network}
	}
	writeFile(t, filepath.Join(dir, "chalkos.yaml"), testInventoryYAML)
	writeFile(t, filepath.Join(dir, "cluster.yaml"), testClusterYAML)
	writeFile(t, filepath.Join(dir, "nodes", "n1.yaml"), strings.Join(docs, "---\n"))
	writeImageDir(t, filepath.Join(dir, "images", "test-metal"), "test", "metal", testRootHash, false)
	return filepath.Join(dir, "chalkos.yaml")
}

// writeImageDir writes what chalkctl reads of an image directory besides the image itself:
// metadata.json of the variant, platform and root hash, the fixture schemas, and with kubernetes
// the image.json of an image with Kubernetes.
func writeImageDir(t *testing.T, dir, variant, platform, rootHash string, kubernetes bool) {
	t.Helper()
	k := "null"
	if kubernetes {
		k = `{"version": "1.37.1", "cni": "none"}`
		writeFile(t, filepath.Join(dir, "image.json"), testImageJSON)
	}
	writeFile(t, filepath.Join(dir, "metadata.json"), fmt.Sprintf(`{"schemaVersion": 0, "id": "chalkos", "version": "0.1.0", "variant": %q, "platform": %q, "rootHash": %q, "kubernetes": %s, "kinds": []}`, variant, platform, rootHash, k))
	copySchemas(t, filepath.Join(dir, "schemas"))
}

// TestValidate checks each node's config against the schemas of its Image's directory, without a
// secrets file or a node, and names every node whose config is invalid.
func TestValidate(t *testing.T) {
	ta := newTestApp(t)
	path := writeInventory(t, ta.dir)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", path}); err != nil {
		t.Fatal(err)
	}
	if want := "n1: valid for the Image test-metal (test on metal, root hash abababababab), config "; !strings.Contains(ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
	// n2's Network misspells a key.
	writeFile(t, path, testInventoryYAML+"    n2: {address: 10.0.0.12, files: [nodes/n2.yaml]}\n")
	writeFile(t, filepath.Join(ta.dir, "nodes", "n2.yaml"), strings.Join([]string{
		strings.Replace(n1Node, "{name: n1}", "{name: n2}", 1), n1SystemDisk,
		"apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {network: {gatway: 10.0.0.1}}\n",
	}, "---\n"))
	ta.stdout.Reset()
	err := ta.run(context.Background(), []string{"validate", "--inventory", path})
	if err == nil || !strings.Contains(err.Error(), "the config of 1 node is invalid") || !strings.Contains(err.Error(), "n2: ") || !strings.Contains(err.Error(), "gatway") {
		t.Errorf("validate = %v, want n2's misspelled key named", err)
	}
	if !strings.Contains(ta.stdout.String(), "n1: valid") {
		t.Errorf("stdout = %q, want n1 valid all the same", ta.stdout)
	}
	if err := ta.run(context.Background(), []string{"validate", "n1", "--inventory", path}); err != nil {
		t.Errorf("validate n1 alone: %v", err)
	}
	if err := ta.run(context.Background(), []string{"validate", "n9", "--inventory", path}); err == nil || !strings.Contains(err.Error(), "has no node n9") {
		t.Errorf("validate n9 = %v", err)
	}
}
