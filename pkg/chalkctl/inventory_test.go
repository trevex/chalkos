package chalkctl

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
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
	ta.withoutSecrets(t)
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
	if err == nil || !strings.Contains(err.Error(), "the config of 1 node is invalid") || !strings.Contains(err.Error(), "\nnode n2: ") || !strings.Contains(err.Error(), "gatway") {
		t.Errorf("validate = %v, want n2's misspelled key named", err)
	}
	if !strings.Contains(ta.stdout.String(), "n1: valid") {
		t.Errorf("stdout = %q, want n1 valid all the same", ta.stdout)
	}
	ta.stdout.Reset()
	if err := ta.run(context.Background(), []string{"validate", "n1", "n1", "--inventory", path}); err != nil || strings.Count(ta.stdout.String(), "n1: valid") != 1 {
		t.Errorf("validate n1 twice: %v, %q, want n1 checked once", err, ta.stdout)
	}
	// An unknown node is a usage error, before any node is checked.
	ta.stdout.Reset()
	if got := ta.execute(context.Background(), "validate", "n1", "n9", "--inventory", path); got != 2 || strings.Contains(ta.stdout.String(), "n1: valid") || !strings.Contains(ta.stderr.String(), "has no node n9") {
		t.Errorf("validate n1 n9 exits with %d, stdout %q, stderr %q", got, ta.stdout, ta.stderr)
	}
}

// TestValidateTheCluster refuses an inventory whose nodes' Cluster documents differ, as every
// other command does.
func TestValidateTheCluster(t *testing.T) {
	dir := t.TempDir()
	path := writeInventory(t, dir)
	// cluster.yaml keeps the Image alone; n1 and n2 each get a Cluster of another endpoint.
	writeFile(t, filepath.Join(dir, "cluster.yaml"), strings.SplitN(testClusterYAML, "---\n", 2)[1])
	cluster := "apiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: lab}\nspec: {endpoint: \"https://%s:6443\"}\n"
	writeFile(t, filepath.Join(dir, "nodes", "n1.yaml"), strings.Join([]string{n1Node, n1SystemDisk, fmt.Sprintf(cluster, "10.0.0.10")}, "---\n"))
	writeFile(t, filepath.Join(dir, "nodes", "n2.yaml"), strings.Join([]string{strings.Replace(n1Node, "{name: n1}", "{name: n2}", 1), n1SystemDisk, fmt.Sprintf(cluster, "10.0.0.20")}, "---\n"))
	writeFile(t, path, testInventoryYAML+"    n2: {address: 10.0.0.12, files: [nodes/n2.yaml]}\n")
	ta := newTestApp(t)
	if err := ta.run(context.Background(), []string{"validate", "n1", "--inventory", path}); err == nil || !strings.Contains(err.Error(), "the nodes n1 and n2 are in different clusters") {
		t.Errorf("validate of nodes in different clusters = %v", err)
	}
}

// TestValidateKubernetes checks a Kubernetes node's config against its image's image.json as the
// node does, and names a node whose Image directory holds no image.
func TestValidateKubernetes(t *testing.T) {
	ta := newTestApp(t)
	path := writeNodes(t, ta.dir, []testNode{{"cp1", identity.KindControlPlane, "k8s"}, {"n1", "", "plain"}}, nil)
	writeImageDir(t, filepath.Join(ta.dir, "images", "k8s-metal"), "k8s", "metal", testRootHash, true)
	err := ta.run(context.Background(), []string{"validate", "--inventory", path})
	if err == nil || !strings.Contains(err.Error(), "the config of 1 node is invalid") ||
		!strings.Contains(err.Error(), "images/plain-metal has no metadata.json; build the image or link its directory there") {
		t.Errorf("validate without n1's image = %v", err)
	}
	if !strings.Contains(ta.stdout.String(), "cp1: valid for the Image k8s-metal (k8s on metal") {
		t.Errorf("stdout = %q, want cp1 valid", ta.stdout)
	}
	// A mirror over plain HTTP is refused where the node reads the cluster's Kubernetes settings.
	mirror := "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: RegistryMirror\nmetadata: {name: local}\nspec: {registry: \"localhost:5000\", mirrors: [\"http://10.0.2.100:5000\"]}\n"
	data, _ := os.ReadFile(filepath.Join(ta.dir, "nodes", "cp1.yaml"))
	writeFile(t, filepath.Join(ta.dir, "nodes", "cp1.yaml"), string(data)+"---\n"+mirror)
	if err := ta.run(context.Background(), []string{"validate", "cp1", "--inventory", path}); err == nil || !strings.Contains(err.Error(), "node cp1: ") || !strings.Contains(err.Error(), "is not an https:// URL") {
		t.Errorf("validate of a plain HTTP mirror = %v", err)
	}
}

// testNode is a node of a test inventory: its name, Kubernetes kind and the variant of its Image.
type testNode struct{ name, kind, variant string }

// writeNodes writes an inventory of the cluster lab with the nodes, the i-th at 10.0.0.<11+i> on
// metal with the Image <variant>-metal, whose directory images gives by variant, else
// images/<variant>-metal below dir. A node of a kind gets a Kubernetes document. It returns the
// inventory's path.
func writeNodes(t *testing.T, dir string, nodes []testNode, images map[string]string) string {
	t.Helper()
	cluster := []string{"apiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: lab}\nspec: {endpoint: \"https://10.0.0.10:6443\"}\n"}
	variants := map[string]bool{}
	inventory := "apiVersion: chalkctl.chalkos.dev/v1alpha1\nkind: Inventory\nmetadata: {name: lab}\nspec:\n  files: [cluster.yaml]\n  nodes:\n"
	for i, n := range nodes {
		node := fmt.Sprintf("apiVersion: chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: %s}\nspec: {imageRef: %s-metal", n.name, n.variant)
		if n.kind != "" {
			node += ", type: " + n.kind
		}
		docs := []string{node + "}\n", n1SystemDisk,
			fmt.Sprintf("apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {network: {address: [10.0.0.%d/24]}}\n", 11+i)}
		if n.kind != "" {
			docs = append(docs, kubernetesDoc)
		}
		writeFile(t, filepath.Join(dir, "nodes", n.name+".yaml"), strings.Join(docs, "---\n"))
		inventory += fmt.Sprintf("    %s: {address: 10.0.0.%d, files: [nodes/%s.yaml]}\n", n.name, 11+i, n.name)
		if !variants[n.variant] {
			variants[n.variant] = true
			image := images[n.variant]
			if image == "" {
				image = filepath.Join(dir, "images", n.variant+"-metal")
			}
			cluster = append(cluster, fmt.Sprintf("apiVersion: chalkos.dev/v1alpha1\nkind: Image\nmetadata: {name: %s-metal}\nspec:\n  source: {directory: %q}\n", n.variant, image))
		}
	}
	writeFile(t, filepath.Join(dir, "cluster.yaml"), strings.Join(cluster, "---\n"))
	writeFile(t, filepath.Join(dir, "chalkos.yaml"), inventory)
	return filepath.Join(dir, "chalkos.yaml")
}

// kubernetesDoc is a Kubernetes document of the kind's defaults.
const kubernetesDoc = "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: kubernetes}\nspec: {}\n"

// inventoryVersion is the version of n1's config as the inventory at path gives it.
func inventoryVersion(t *testing.T, path string) string {
	t.Helper()
	c, err := loadInventory(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.version("n1")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestInstallFromTheInventory installs n1 in place with the config the inventory gives it, which
// carries no root hash of its image, and a node certificate that names its hostname and static
// address.
func TestInstallFromTheInventory(t *testing.T) {
	ta := newTestApp(t)
	path := writeInventory(t, ta.dir, strings.Replace(n1Node, "{imageRef: test-metal}", "{imageRef: test-metal, hostname: n1.lab}", 1), n1SystemDisk, n1Network)
	s := maintenanceNode()
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request) error { got = req; return nil }
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), []string{"install", "n1", "--insecure", "--inventory", path, "--address", addr}); err != nil {
		t.Fatal(err)
	}
	if got.Config == nil || got.Config.Version != inventoryVersion(t, path) {
		t.Fatalf("install request config = %+v, want the inventory's", got.Config)
	}
	for _, d := range got.Config.Documents {
		if _, ok := d.Object["spec"].(map[string]any)["rootHash"]; d.Kind == "Image" && ok {
			t.Errorf("the node's Image names a root hash: %v", d.Object)
		}
	}
	cert, err := pki.VerifyNode(string(got.NodeCertificate), string(got.NodeKey), ta.secrets.OSCA.Certificate, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Leaf.VerifyHostname("n1.lab"); err != nil {
		t.Errorf("node certificate: %v", err)
	}
	if ips := cert.Leaf.IPAddresses; len(ips) != 1 || !ips[0].Equal(net.ParseIP("10.0.0.11")) {
		t.Errorf("node certificate addresses = %v, want the static address", ips)
	}
}

// TestInstallTakesTheImageOfItsImageDocument sends the installer the image the node's Image names
// when --image names none, and refuses an image directory whose metadata.json names another
// build than its image.
func TestInstallTakesTheImageOfItsImageDocument(t *testing.T) {
	ta := newTestApp(t)
	img := testUpgradeImage(t, "test", "0.1.0")
	writeFile(t, filepath.Join(img, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	path := writeInventory(t, ta.dir)
	writeFile(t, filepath.Join(ta.dir, "cluster.yaml"), strings.Replace(testClusterYAML, "directory: images/test-metal", "directory: "+img, 1))
	s := maintenanceNode()
	s.Installer = true
	var got install.PartsRequest
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		_, err := io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	args := []string{"install", "n1", "--fingerprint", s.Fingerprint, "--inventory", path, "--address", addr}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if got.Image.Version != "0.1.0" || got.Config == nil {
		t.Errorf("install request = %+v", got)
	}
	md, _ := os.ReadFile(filepath.Join(img, "metadata.json"))
	writeFile(t, filepath.Join(img, "metadata.json"), strings.Replace(string(md), hex.EncodeToString(got.Image.RootHash), testRootHash, 1))
	if err := ta.run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "metadata.json names the root hash "+testRootHash) {
		t.Errorf("an image of another build = %v", err)
	}
}

// TestStatusComparesWithTheInventory reports whether a node runs the config the inventory gives
// it, which a new build of its image leaves as it is, and the platform of the node's Image.
func TestStatusComparesWithTheInventory(t *testing.T) {
	ta := newTestApp(t)
	path := writeInventory(t, ta.dir)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	args := func(cmd ...string) []string { return append(cmd, "--inventory", path, "--address", addr) }
	if err := ta.run(context.Background(), args("apply-config", "n1")); err != nil {
		t.Fatal(err)
	}
	ta.stdout.Reset()
	if err := ta.run(context.Background(), args("status", "n1")); err != nil {
		t.Fatal(err)
	}
	if out := ta.stdout.String(); !strings.Contains(out, "config "+inventoryVersion(t, path)+" (the inventory's)") || !strings.Contains(out, "platform metal (the inventory's)") {
		t.Errorf("status = %q", out)
	}
	// A new build of the image leaves the node's config the inventory's: the root hash is no part
	// of it.
	version := inventoryVersion(t, path)
	writeImageDir(t, filepath.Join(ta.dir, "images", "test-metal"), "test", "metal", strings.Repeat("cd", 32), false)
	ta.stdout.Reset()
	if err := ta.run(context.Background(), args("status", "n1")); err != nil {
		t.Fatal(err)
	}
	if out := ta.stdout.String(); inventoryVersion(t, path) != version || !strings.Contains(out, "config "+version+" (the inventory's)") {
		t.Errorf("status after a new build of the image = %q", out)
	}
	writeFile(t, filepath.Join(ta.dir, "nodes", "n1.yaml"), strings.Join([]string{n1Node, n1SystemDisk, strings.Replace(n1Network, "10.0.0.11", "10.0.0.12", 1)}, "---\n"))
	ta.stdout.Reset()
	if err := ta.run(context.Background(), args("status", "n1")); err != nil {
		t.Fatal(err)
	}
	if out := ta.stdout.String(); !strings.Contains(out, "(not the inventory's; chalkctl apply-config n1 delivers it)") {
		t.Errorf("status after a change = %q", out)
	}
}

// TestImageLine names a store other than the one of the image the node's config names.
func TestImageLine(t *testing.T) {
	c, err := loadInventory(writeInventory(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	same, _ := hex.DecodeString(testRootHash)
	if got := c.imageLine("n1", &nodev1.BootStatus{RootHash: same}); got != "" {
		t.Errorf("the line of the image the config names = %q", got)
	}
	got := c.imageLine("n1", &nodev1.BootStatus{RootHash: bytes.Repeat([]byte{0xcd}, 32)})
	if !strings.Contains(got, "not that of the Image test-metal its config names, "+testRootHash) {
		t.Errorf("the line of another image = %q", got)
	}
}

// TestAddressFromTheInventory reaches a node at --address, else at the inventory's address, and
// asks for --address without either.
func TestAddressFromTheInventory(t *testing.T) {
	dir := t.TempDir()
	path := writeInventory(t, dir)
	c, err := loadInventory(path)
	if err != nil {
		t.Fatal(err)
	}
	if addr, source, err := c.address("", nil, "n1"); err != nil || addr != "10.0.0.11" || source != "the inventory "+path {
		t.Errorf("address = %q, %q, %v", addr, source, err)
	}
	if addr, source, err := c.address("10.9.9.9", nil, "n1"); err != nil || addr != "10.9.9.9" || source != "--address" {
		t.Errorf("address with --address = %q, %q, %v", addr, source, err)
	}
	writeFile(t, path, strings.Replace(testInventoryYAML, "address: 10.0.0.11, ", "", 1))
	if c, err = loadInventory(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.address("", nil, "n1"); err == nil || !strings.Contains(err.Error(), "names no address of n1; pass --address") {
		t.Errorf("address without one = %v", err)
	}
}

// TestSecretsOnlyFromTheFlag reads the secrets file --secrets names, or standard input for -,
// and no secrets file beside the inventory.
func TestSecretsOnlyFromTheFlag(t *testing.T) {
	ta := newTestApp(t)
	path := writeInventory(t, ta.dir)
	want, _ := pki.RecoveryKey(ta.secrets.RecoverySecret, "lab", "n1")
	recoveryKey := []string{"recovery-key", "n1", "--inventory", path}
	// The app's own run names no secrets file of the test's.
	if err := ta.app.run(context.Background(), recoveryKey); err == nil || !strings.Contains(err.Error(), "--secrets") {
		t.Errorf("recovery-key with secrets.json beside the inventory = %v, want --secrets named", err)
	}
	data, err := os.ReadFile(filepath.Join(ta.dir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secrets := range []string{filepath.Join(ta.dir, "secrets.json"), "-"} {
		ta.stdin = bytes.NewReader(data)
		ta.stdout.Reset()
		if err := ta.app.run(context.Background(), append(recoveryKey, "--secrets", secrets)); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(ta.stdout.String()); got != want {
			t.Errorf("recovery key from %s = %q, want %q", secrets, got, want)
		}
	}
}

// TestNodesOfTheInventory lists nodes by the type their Node documents give them.
func TestNodesOfTheInventory(t *testing.T) {
	c, err := loadInventory(writeNodes(t, t.TempDir(), []testNode{
		{"cp3", identity.KindControlPlane, "k8s"}, {"cp1", identity.KindControlPlane, "k8s"},
		{"w1", identity.KindWorker, "k8s"}, {"n1", "", "plain"},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.nodesOf(identity.KindControlPlane, "cp3"); err != nil || !slices.Equal(got, []string{"cp1"}) {
		t.Errorf("control planes but cp3 = %q, %v", got, err)
	}
	if got, err := c.nodesOf("", ""); err != nil || !slices.Equal(got, []string{"n1"}) {
		t.Errorf("nodes without Kubernetes = %q, %v", got, err)
	}
}

// TestLoadClusterRefusesBoth refuses an inventory and a manifest together.
func TestLoadClusterRefusesBoth(t *testing.T) {
	path := writeInventory(t, t.TempDir())
	if _, err := loadCluster(clusterFlags{inventory: path, manifest: "m.json"}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("an inventory and a manifest = %v", err)
	}
}

// TestStatusWithTheUsersClientFile reaches a node of the inventory with the user's client file
// when no secrets file is named, and without one names --config and --secrets.
func TestStatusWithTheUsersClientFile(t *testing.T) {
	ta := newTestApp(t)
	path := writeInventory(t, ta.dir)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	status := []string{"status", "n1", "--inventory", path, "--address", addr}
	// The app's own run names no secrets file of the test's.
	if err := ta.app.run(context.Background(), status); err == nil || !strings.Contains(err.Error(), "pass --config or --secrets") {
		t.Errorf("status without a secrets file or a client file = %v, want --config and --secrets named", err)
	}
	reader := ta.writeConfig(t, "alice", pki.RoleReader, 24*time.Hour, time.Now())
	if err := os.MkdirAll(filepath.Dir(defaultConfigPath(ta.home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(reader, defaultConfigPath(ta.home)); err != nil {
		t.Fatal(err)
	}
	ta.stdout.Reset()
	if err := ta.app.run(context.Background(), status); err != nil || !strings.Contains(ta.stdout.String(), "(the inventory's)") {
		t.Errorf("status with a reader's client file = %v, %q", err, ta.stdout)
	}
}

// TestCommandsThatNeedAnInventory refuse a cluster without an inventory before they read any
// credentials, and an upgrade without --image says that only an inventory names the images.
func TestCommandsThatNeedAnInventory(t *testing.T) {
	ta := newTestApp(t)
	ta.withoutSecrets(t)
	for _, args := range [][]string{
		{"install", "n1", "--insecure"}, {"apply-config", "n1"}, {"storage", "reset", "n1", "var"}, {"node", "renew", "n1"},
	} {
		err := ta.run(context.Background(), append(args, "--address", "127.0.0.1:1"))
		if err == nil || !strings.Contains(err.Error(), "needs an inventory") {
			t.Errorf("%s = %v, want a refusal naming the inventory", strings.Join(args, " "), err)
		}
	}
	reader := ta.writeConfig(t, "alice", pki.RoleReader, 24*time.Hour, time.Now())
	if err := ta.run(context.Background(), []string{"upgrade", "--config", reader}); err == nil || !strings.Contains(err.Error(), "which only an inventory gives") {
		t.Errorf("upgrade without --image or an inventory = %v", err)
	}
}

// TestImageLineOfAStagedImage advises a reboot to a node that has the image of its Image staged,
// and says why the store is not compared when the Image's directory cannot be read.
func TestImageLineOfAStagedImage(t *testing.T) {
	dir := t.TempDir()
	c, err := loadInventory(writeInventory(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	other := bytes.Repeat([]byte{0xcd}, 32)
	if got := c.imageLine("n1", &nodev1.BootStatus{RootHash: other, Staged: "0.1.0"}); !strings.Contains(got, "it has 0.1.0 staged, which it boots with its next reboot: chalkctl reboot n1") {
		t.Errorf("the line of a staged image = %q", got)
	}
	if err := os.Remove(filepath.Join(dir, "images", "test-metal", "metadata.json")); err != nil {
		t.Fatal(err)
	}
	if c, err = loadInventory(filepath.Join(dir, "chalkos.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := c.imageLine("n1", &nodev1.BootStatus{RootHash: other}); !strings.Contains(got, "its store is not compared with the image of its config: ") || !strings.Contains(got, "has no metadata.json") {
		t.Errorf("the line without the Image's directory = %q", got)
	}
}
