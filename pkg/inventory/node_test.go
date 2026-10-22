package inventory

import (
	"encoding/json"
	"fmt"
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

const clusterFiles = `apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  endpoint: https://10.0.0.11:6443
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: monitoring}
spec:
  objects: []
`

// nodeFile is a node's file: its Kubernetes Node of the type, its Host of a hostname in .lab, and
// its uplink at the address.
func nodeFile(name, nodeType, address string) string {
	return "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: default}\nspec: {type: " + nodeType + "}\n---\n" +
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
	if got := kinds(cp1.Documents); !slices.Equal(got, []string{"Cluster/default", "Addon/monitoring", "Node/default", "Host/default", "Network/10-uplink"}) {
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
	if got := kinds(w1.Documents); !slices.Equal(got, []string{"Cluster/default", "Node/default", "Host/default", "Network/10-uplink"}) {
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

// TestNodeWithoutAKubernetesNode takes a node whose files hold no Kubernetes Node, as a node of an
// image without Kubernetes has, without a type and without the Addon, and a Node of any name.
func TestNodeWithoutAKubernetesNode(t *testing.T) {
	dir := homelabDir(t)
	write(t, filepath.Join(dir, "nodes", "cp1.yaml"), "apiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {}\n")
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := inv.Node("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(cp1.Documents); cp1.Type != "" || !slices.Equal(got, []string{"Cluster/default", "Network/10-uplink"}) {
		t.Errorf("type = %q, documents = %q, want no type and no Addon", cp1.Type, got)
	}
	write(t, filepath.Join(dir, "nodes", "cp1.yaml"), strings.Replace(nodeFile("cp1", "controlplane", "10.0.0.11"), "{name: default}", "{name: cp2}", 1))
	if inv, err = Load(filepath.Join(dir, "chalkos.yaml")); err != nil {
		t.Fatal(err)
	}
	if cp1, err = inv.Node("cp1"); err != nil || cp1.Type != "controlplane" || !slices.Contains(kinds(cp1.Documents), "Addon/monitoring") {
		t.Errorf("a Node named cp2: %+v, %v, want a control plane with the Addon", cp1, err)
	}
}

// TestNodeRefuses refuses a node whose files hold a document twice.
func TestNodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		file, content, want string
	}{
		"a document twice": {"nodes/cp1.yaml", nodeFile("cp1", "controlplane", "10.0.0.11") + "---\n" + clusterDoc("default", "https://10.0.0.11:6443"),
			`cluster/all.yaml and nodes/cp1.yaml both hold Cluster "default"`},
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

// clusterDoc is a Kubernetes Cluster document called name that names the endpoint.
func clusterDoc(name, endpoint string) string {
	return "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: " + name + "}\nspec: {endpoint: \"" + endpoint + "\"}\n"
}

// flannelDoc is a Flannel document called name with the MTU.
func flannelDoc(name, mtu string) string {
	return "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Flannel\nmetadata: {name: " + name + "}\nspec: {mtu: " + mtu + "}\n"
}

// TestEndpoint takes the endpoint of the Cluster documents, which every node holding one holds
// with the same spec, as it holds the same Flannel spec or none, whatever the documents' names;
// nodes without a Cluster document are skipped. Nodes that differ, and a node holding two, are
// refused, and the message names the nodes of each spec.
func TestEndpoint(t *testing.T) {
	dir := homelabDir(t)
	load := func() *Inventory {
		t.Helper()
		inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}
	// files writes the nodes' own files, each with the documents given after its own.
	files := func(cp1, w1 string) {
		t.Helper()
		write(t, filepath.Join(dir, "nodes", "cp1.yaml"), nodeFile("cp1", "controlplane", "10.0.0.11")+cp1)
		write(t, filepath.Join(dir, "nodes", "w1.yaml"), nodeFile("w1", "worker", "10.0.0.21")+w1)
	}
	if endpoint, err := load().Endpoint(); err != nil || endpoint != "https://10.0.0.11:6443" {
		t.Errorf("endpoint = %q, %v", endpoint, err)
	}
	// Each node holds its own documents from here on.
	write(t, filepath.Join(dir, "cluster", "all.yaml"), "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Addon\nmetadata: {name: monitoring}\nspec: {objects: []}\n")
	for name, tc := range map[string]struct{ cp1, w1, want string }{
		"the same documents of other names": {
			"---\n" + clusterDoc("default", "https://10.0.0.11:6443") + "---\n" + flannelDoc("default", "1400"),
			"---\n" + clusterDoc("cluster", "https://10.0.0.11:6443") + "---\n" + flannelDoc("flannel", "1400"), ""},
		"a node without Kubernetes": {"---\n" + clusterDoc("default", "https://10.0.0.11:6443") + "---\n" + flannelDoc("default", "1400"), "", ""},
		"another endpoint": {"---\n" + clusterDoc("default", "https://10.0.0.11:6443"), "---\n" + clusterDoc("default", "https://10.0.0.99:6443"),
			`the nodes hold different Cluster specs: spec.endpoint is "https://10.0.0.11:6443" on cp1, "https://10.0.0.99:6443" on w1; every node of the cluster holds the same Cluster and Flannel specs`},
		"another MTU": {
			"---\n" + clusterDoc("default", "https://10.0.0.11:6443") + "---\n" + flannelDoc("default", "1400"),
			"---\n" + clusterDoc("default", "https://10.0.0.11:6443") + "---\n" + flannelDoc("default", "1450"),
			"the nodes hold different Flannel specs: spec.mtu is 1400 on cp1, 1450 on w1; every node of the cluster holds the same Cluster and Flannel specs"},
		"a Flannel document on one node": {
			"---\n" + clusterDoc("default", "https://10.0.0.11:6443"),
			"---\n" + clusterDoc("default", "https://10.0.0.11:6443") + "---\n" + flannelDoc("default", "null"),
			"the nodes hold different Flannel specs: none on cp1, one on w1; every node of the cluster holds the same Cluster and Flannel specs"},
		"two Cluster documents": {"---\n" + clusterDoc("default", "https://10.0.0.11:6443"),
			"---\n" + clusterDoc("a", "https://10.0.0.11:6443") + "---\n" + clusterDoc("b", "https://10.0.0.11:6443"),
			"node w1: its files hold 2 Cluster documents, not one"},
	} {
		t.Run(name, func(t *testing.T) {
			files(tc.cp1, tc.w1)
			endpoint, err := load().Endpoint()
			switch {
			case tc.want == "" && (err != nil || endpoint != "https://10.0.0.11:6443"):
				t.Errorf("endpoint = %q, %v", endpoint, err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// Without a Cluster document no node names an endpoint.
	files("", "")
	if endpoint, err := load().Endpoint(); err != nil || endpoint != "" {
		t.Errorf("endpoint without Cluster documents = %q, %v", endpoint, err)
	}
	// An inventory without nodes takes its endpoint from the shared files.
	write(t, filepath.Join(dir, "cluster", "all.yaml"), clusterFiles)
	write(t, filepath.Join(dir, "chalkos.yaml"), "apiVersion: chalkctl.chalkos.dev/v1alpha1\nkind: Inventory\nmetadata: {name: homelab}\nspec:\n  files: [cluster/*.yaml]\n")
	if endpoint, err := load().Endpoint(); err != nil || endpoint != "https://10.0.0.11:6443" {
		t.Errorf("endpoint without nodes = %q, %v", endpoint, err)
	}
}

// TestEndpointNamesTheNodesOfEachSpec lists the nodes of each spec in name order, the spec of the
// first node first.
func TestEndpointNamesTheNodesOfEachSpec(t *testing.T) {
	dir := homelabDir(t)
	write(t, filepath.Join(dir, "cluster", "all.yaml"), "")
	nodes := map[string]string{"cp1": "10", "cp2": "10", "w1": "20", "w2": "10", "w3": "30"}
	inventory := strings.Replace(cluster, "    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}\n    w1: {address: 10.0.0.21, files: [nodes/w1.yaml]}\n", "", 1)
	for name, host := range nodes {
		inventory += "    " + name + ": {files: [nodes/" + name + ".yaml]}\n"
		write(t, filepath.Join(dir, "nodes", name+".yaml"), clusterDoc("default", "https://10.0.0."+host+":6443"))
	}
	write(t, filepath.Join(dir, "chalkos.yaml"), inventory)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := `the nodes hold different Cluster specs: spec.endpoint is "https://10.0.0.10:6443" on cp1, cp2 and w2, "https://10.0.0.20:6443" on w1, "https://10.0.0.30:6443" on w3; every node of the cluster holds the same Cluster and Flannel specs`
	if _, err := inv.Endpoint(); err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
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
		t.Errorf("w1's Kubernetes endpoint = %v after a change to cp1's", got)
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

// TestAlikeNamesTheFirstDifference names the first field that differs in a walk of the fields in
// name order, below objects and into lists of one length, with each value and its nodes; a field
// a spec lacks is unset. Flannel's vxlanSourceSubnets compare as a set.
func TestAlikeNamesTheFirstDifference(t *testing.T) {
	parse := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	nodes := []string{"cp1", "cp2", "w1"}
	for name, tc := range map[string]struct {
		kind  string
		specs [3]string
		want  string
	}{
		"equal": {"Cluster", [3]string{`{"domain": "a"}`, `{"domain": "a"}`, `{"domain": "a"}`}, ""},
		"a field": {"Cluster", [3]string{`{"domain": "a"}`, `{"domain": "a"}`, `{"domain": "b"}`},
			`spec.domain is "a" on cp1 and cp2, "b" on w1`},
		"the first field by name": {"Cluster", [3]string{`{"domain": "a", "endpoint": "x"}`, `{"domain": "a", "endpoint": "x"}`, `{"domain": "b", "endpoint": "y"}`},
			`spec.domain is "a" on cp1 and cp2, "b" on w1`},
		"a nested field": {"Cluster", [3]string{`{"vip": {"addresses": ["10.0.0.10"], "mode": "l2"}}`, `{"vip": {"addresses": ["10.0.0.10"], "mode": "l2"}}`, `{"vip": {"addresses": ["10.0.0.20"], "mode": "l2"}}`},
			`spec.vip.addresses[0] is "10.0.0.10" on cp1 and cp2, "10.0.0.20" on w1`},
		"lists of other lengths": {"Cluster", [3]string{`{"ipFamilies": ["ipv4"]}`, `{"ipFamilies": ["ipv4", "ipv6"]}`, `{"ipFamilies": ["ipv4"]}`},
			`spec.ipFamilies is ["ipv4"] on cp1 and w1, ["ipv4","ipv6"] on cp2`},
		"a field one spec lacks": {"Cluster", [3]string{`{"domain": "cluster.local"}`, `{}`, `{}`},
			`spec.domain is "cluster.local" on cp1, unset on cp2 and w1`},
		"VXLAN sources in another order": {"Flannel", [3]string{`{"vxlanSourceSubnets": ["10.0.0.0/24", "fd00::/64"]}`, `{"vxlanSourceSubnets": ["fd00::/64", "10.0.0.0/24"]}`, `{"vxlanSourceSubnets": ["10.0.0.0/24", "fd00::/64"]}`}, ""},
		"other VXLAN sources": {"Flannel", [3]string{`{"vxlanSourceSubnets": ["10.0.0.0/24", "fd00::/64"]}`, `{"vxlanSourceSubnets": ["fd00::/64", "10.0.0.0/24"]}`, `{"vxlanSourceSubnets": ["10.0.1.0/24", "fd00::/64"]}`},
			`spec.vxlanSourceSubnets[0] is "10.0.0.0/24" on cp1 and cp2, "10.0.1.0/24" on w1`},
	} {
		t.Run(name, func(t *testing.T) {
			specs := map[string]map[string]any{}
			for i, n := range nodes {
				specs[n] = parse(tc.specs[i])
			}
			err := alike(tc.kind, nodes, func(n string) map[string]any { return specs[n] })
			want := ""
			if tc.want != "" {
				want = "the nodes hold different " + tc.kind + " specs: " + tc.want + "; every node of the cluster holds the same Cluster and Flannel specs"
			}
			if got := fmt.Sprint(err); (err == nil) != (want == "") || err != nil && got != want {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
}
