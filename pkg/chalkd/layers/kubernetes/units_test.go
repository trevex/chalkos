package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// runtimeDocs writes the documents of yaml below a new run directory, as chalkd writes them.
func runtimeDocs(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	docs, err := config.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.WriteRuntime(dir, docs, nil, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

// tree is each file below dir by its path relative to dir, with its contents.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

const mirrors = `apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: lan}
spec: {host: "10.0.0.5:5000", insecure: true}
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: hub}
spec: {registry: docker.io, endpoints: ["http://10.0.0.5:5000"]}
`

// registry-mirrors writes each registry's hosts.toml in a directory of its own, replacing the
// files of an earlier run, and leaves nothing next to the directory.
func TestRegistryMirrors(t *testing.T) {
	runDir := runtimeDocs(t, mirrors)
	parent := filepath.Join(t.TempDir(), "containerd")
	certs := filepath.Join(parent, "certs.d")
	write(t, filepath.Join(certs, "stale", "hosts.toml"), "server = \"https://stale.example\"\n")
	if err := registryMirrors(runDir, certs); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"10.0.0.5:5000/hosts.toml": "server = \"http://10.0.0.5:5000\"\n",
		"docker.io/hosts.toml":     "server = \"https://registry-1.docker.io\"\n\n[host.\"http://10.0.0.5:5000\"]\n  capabilities = [\"pull\", \"resolve\"]\n",
	}
	if got := tree(t, certs); !maps.Equal(got, want) {
		t.Errorf("certs.d = %q, want %q", got, want)
	}
	if info, err := os.Stat(certs); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("certs.d: %v, %v", info, err)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 1 {
		t.Errorf("left next to certs.d: %v", entries)
	}
}

// A document chalkd cannot read leaves the files as they were, and nothing next to them.
func TestRegistryMirrorsKeepsTheFilesOnAFailure(t *testing.T) {
	runDir := runtimeDocs(t, mirrors)
	root, err := filepath.EvalSymlinks(filepath.Join(runDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "oci.chalkos.dev", "Mirror", "hub.json"), "{")
	parent := t.TempDir()
	certs := filepath.Join(parent, "certs.d")
	write(t, filepath.Join(certs, "docker.io", "hosts.toml"), "old\n")
	if err := registryMirrors(runDir, certs); err == nil {
		t.Fatal("an unreadable document was taken")
	}
	if got := tree(t, certs); !maps.Equal(got, map[string]string{"docker.io/hosts.toml": "old\n"}) {
		t.Errorf("certs.d = %q", got)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 1 {
		t.Errorf("left next to certs.d: %v", entries)
	}
}

// Without documents, and without an earlier run, registry-mirrors makes the directory empty.
func TestRegistryMirrorsWithoutDocuments(t *testing.T) {
	certs := filepath.Join(t.TempDir(), "containerd", "certs.d")
	if err := registryMirrors(t.TempDir(), certs); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(certs); err != nil || len(entries) != 0 {
		t.Errorf("certs.d = %v, %v", entries, err)
	}
}

// The cluster registry's endpoints come before the Mirror's, the one the node serves first, each
// below the mirrored host's path, verified by the OS CAs and presented the node certificate, and
// a mirrored host that no Mirror names gets a hosts.toml.
func TestRegistryHostsClusterRegistry(t *testing.T) {
	runDir := runtimeDocs(t, `apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lan}
spec: {network: {address: [192.0.2.12/24]}}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: ClusterRegistry
metadata: {name: cluster}
spec: {mirrors: [docker.io, registry.k8s.io], endpoints: ["fd00::11", "192.0.2.12"], port: 5000}
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Mirror
metadata: {name: hub}
spec: {registry: docker.io, endpoints: ["https://mirror.example"]}
`)
	got, err := registryHosts(runDir)
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "\n[host.\"https://%s:5000/v2/%s\"]\n  capabilities = [\"pull\", \"resolve\"]\n  override_path = true\n  ca = \"/state/chalkd/ca.crt\"\n  client = \"/state/chalkd/node.pem\"\n"
	want := map[string]string{
		"docker.io": "server = \"https://registry-1.docker.io\"\n" +
			fmt.Sprintf(endpoint, "192.0.2.12", "docker.io") +
			fmt.Sprintf(endpoint, "[fd00::11]", "docker.io") +
			"\n[host.\"https://mirror.example\"]\n  capabilities = [\"pull\", \"resolve\"]\n",
		"registry.k8s.io": "server = \"https://registry.k8s.io\"\n" +
			fmt.Sprintf(endpoint, "192.0.2.12", "registry.k8s.io") +
			fmt.Sprintf(endpoint, "[fd00::11]", "registry.k8s.io"),
	}
	if !maps.Equal(got, want) {
		t.Errorf("hosts = %q, want %q", got, want)
	}
}

func nodeDocs(t *testing.T, typ string) string {
	t.Helper()
	return runtimeDocs(t, "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: default}\nspec: {type: "+typ+"}\n")
}

// A control plane gets etcd's directory, readable by root alone, and the table that marks its
// ports, replaced in one transaction.
func TestControlPlane(t *testing.T) {
	etcd := filepath.Join(t.TempDir(), "lib", "etcd")
	r := &tooltest.Runner{}
	if err := controlPlane(context.Background(), r, nodeDocs(t, "controlplane"), etcd, "0x02000000"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(etcd); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("etcd's directory: %v, %v", info, err)
	}
	if !slices.Equal(r.Calls, []string{"nft -f -"}) {
		t.Fatalf("calls = %q", r.Calls)
	}
	const want = `table inet chalkos-controlplane
delete table inet chalkos-controlplane
table inet chalkos-controlplane {
  chain input {
    type filter hook input priority filter - 1; policy accept;
    meta mark & 0x02000000 == 0x02000000 meta mark set meta mark ^ 0x02000000
    tcp dport { 6443, 2379, 2380 } meta mark set meta mark | 0x02000000
  }
  chain clear {
    type filter hook input priority filter + 1; policy accept;
    meta mark & 0x02000000 == 0x02000000 meta mark set meta mark ^ 0x02000000
  }
}
`
	if got := string(r.Cmds[0].Stdin); got != want {
		t.Errorf("ruleset:\n%s\nwant:\n%s", got, want)
	}
}

// An etcd directory that exists becomes readable by root alone.
func TestControlPlaneKeepsEtcd(t *testing.T) {
	etcd := filepath.Join(t.TempDir(), "etcd")
	write(t, filepath.Join(etcd, "member", "snap"), "data")
	if err := os.Chmod(etcd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := controlPlane(context.Background(), &tooltest.Runner{}, nodeDocs(t, "controlplane"), etcd, ""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(etcd); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("etcd's directory: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(etcd, "member", "snap")); err != nil {
		t.Error(err)
	}
}

// A worker deletes the table when it has one, and makes no etcd directory.
func TestControlPlaneWorker(t *testing.T) {
	for name, tc := range map[string]struct {
		rules []tooltest.Rule
		want  []string
	}{
		"with the table": {want: []string{"nft list table inet chalkos-controlplane", "nft delete table inet chalkos-controlplane"}},
		"without the table": {
			rules: []tooltest.Rule{{Prefix: "nft list", Err: errors.New("no such table")}},
			want:  []string{"nft list table inet chalkos-controlplane"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			etcd := filepath.Join(t.TempDir(), "etcd")
			r := &tooltest.Runner{Rules: tc.rules}
			if err := controlPlane(context.Background(), r, nodeDocs(t, "worker"), etcd, "0x02000000"); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.Calls, tc.want) {
				t.Errorf("calls = %q, want %q", r.Calls, tc.want)
			}
			if !r.Cmds[0].Quiet {
				t.Error("the table's listing is not quiet")
			}
			if _, err := os.Stat(etcd); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a worker's etcd directory: %v", err)
			}
		})
	}
}

// Without the firewall's mark, no table is touched; a node without a Node document is no control
// plane.
func TestControlPlaneWithoutFirewall(t *testing.T) {
	etcd := filepath.Join(t.TempDir(), "etcd")
	r := &tooltest.Runner{}
	if err := controlPlane(context.Background(), r, t.TempDir(), etcd, ""); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("calls = %q", r.Calls)
	}
	if _, err := os.Stat(etcd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("etcd's directory without a Node: %v", err)
	}
}

// A refused ruleset and a failed deletion fail the unit.
func TestControlPlaneFails(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "nft -f", Err: errors.New("refused")}}}
	if err := controlPlane(context.Background(), r, nodeDocs(t, "controlplane"), filepath.Join(t.TempDir(), "etcd"), "0x02000000"); err == nil {
		t.Error("a refused ruleset did not fail")
	}
	r = &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "nft delete", Err: errors.New("busy")}}}
	if err := controlPlane(context.Background(), r, nodeDocs(t, "worker"), filepath.Join(t.TempDir(), "etcd"), "0x02000000"); err == nil {
		t.Error("a failed deletion did not fail")
	}
}

// The firewall's mark is a single bit.
func TestParseMark(t *testing.T) {
	if got, err := parseMark("0x2000000"); err != nil || got != "0x02000000" {
		t.Errorf("parseMark = %q, %v", got, err)
	}
	for _, s := range []string{"", "0", "0x3", "mark", "0x100000000", "1; flush ruleset"} {
		if _, err := parseMark(s); err == nil {
			t.Errorf("parseMark(%q) accepted", s)
		}
	}
}
