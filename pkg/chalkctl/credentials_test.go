package chalkctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/pki"
)

// writeConfig writes a client file of the test cluster for the role, valid for validity from
// now, and returns its path.
func (ta *testApp) writeConfig(t *testing.T, name, role string, validity time.Duration, now time.Time) string {
	t.Helper()
	c, err := client.NewConfig(ta.secrets.OSCA, ta.secrets.OSCABundle(), "lab", name, role, validity, map[string]string{"n1": "10.0.0.11"}, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ta.dir, name+".json")
	writeFile(t, path, string(data))
	return path
}

// withoutSecrets removes the secrets file from the flake directory.
func (ta *testApp) withoutSecrets(t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join(ta.dir, "secrets.json")); err != nil {
		t.Fatal(err)
	}
}

func TestConfigNew(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "alice.json")
	args := []string{"config", "new", "--name", "alice", "--role", "operator", "--ttl", "720h", "--out", out, "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	c, err := client.ReadConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := c.Leaf()
	if c.Cluster != "lab" || c.Name != "alice" || c.Role != pki.RoleOperator || c.OSCA != ta.secrets.OSCA.Certificate || c.Nodes["n1"] != "10.0.0.11" {
		t.Errorf("client file %v with nodes %v", c, c.Nodes)
	}
	if got := time.Until(leaf.NotAfter).Round(time.Hour); got != 720*time.Hour {
		t.Errorf("the certificate lasts %v", got)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	if err := ta.run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("err = %v, want a refusal to overwrite naming --force", err)
	}
	if err := ta.run(context.Background(), append(args, "--force")); err != nil {
		t.Errorf("--force: %v", err)
	}
	// By default the file goes where chalkctl looks for it.
	if err := ta.run(context.Background(), []string{"config", "new", "--name", "bob", "--role", "reader", "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir}); err != nil {
		t.Fatal(err)
	}
	if c, err := client.ReadConfig(defaultConfigPath(ta.home)); err != nil || c.Name != "bob" {
		t.Errorf("default client file: %v, %v", c, err)
	}
	for _, bad := range [][]string{{"--name", "x", "--role", "root"}, {"--role", "reader"}, {"--name", "x", "--role", "node"}} {
		args := append([]string{"config", "new", "--out", filepath.Join(ta.dir, "bad.json"), "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir}, bad...)
		if err := ta.run(context.Background(), args); err == nil {
			t.Errorf("config new %v: accepted", bad)
		}
	}
}

func TestClientFileRole(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	ta.withoutSecrets(t)

	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", reader}, addr)); err != nil {
		t.Errorf("status as a reader: %v", err)
	}
	if err := ta.run(context.Background(), ta.args([]string{"reboot", "n1", "--config", reader}, addr)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("reboot as a reader: %v, want permission denied", err)
	}
	// Commands that need the secrets file do not take a client file.
	if err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1", "--config", reader}, addr)); err == nil {
		t.Error("apply-identity ran with a client file")
	}
}

func TestCredentialChoice(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	t.Setenv("CHALKOSCONFIG", reader)
	reboot := ta.args([]string{"reboot", "n1"}, addr)

	// $CHALKOSCONFIG wins over the secrets file found in the flake directory.
	if err := ta.run(context.Background(), reboot); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("with $CHALKOSCONFIG and secrets.json in the directory: %v, want the reader refused", err)
	}
	// --secrets wins over $CHALKOSCONFIG.
	if err := ta.run(context.Background(), append(reboot, "--secrets", filepath.Join(ta.dir, "secrets.json"))); err != nil {
		t.Errorf("with --secrets: %v", err)
	}
	// --config wins over --secrets.
	if err := ta.run(context.Background(), append(reboot, "--config", reader, "--secrets", filepath.Join(ta.dir, "secrets.json"))); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("with --config: %v, want the reader refused", err)
	}
	// Without $CHALKOSCONFIG, the secrets file found in the directory.
	t.Setenv("CHALKOSCONFIG", "")
	if err := ta.run(context.Background(), reboot); err != nil {
		t.Errorf("with secrets.json in the directory: %v", err)
	}
	// Without a secrets file, $CHALKOSCONFIG, else ~/.config/chalkos/config.
	ta.withoutSecrets(t)
	t.Setenv("CHALKOSCONFIG", reader)
	if err := ta.run(context.Background(), reboot); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("with $CHALKOSCONFIG: %v, want the reader refused", err)
	}
	t.Setenv("CHALKOSCONFIG", "")
	if err := ta.run(context.Background(), reboot); err == nil || !strings.Contains(err.Error(), "--config") {
		t.Errorf("without credentials: %v, want an error naming --config", err)
	}
	operator := ta.writeConfig(t, "bob", pki.RoleOperator, time.Hour*24*365, time.Now())
	data, _ := os.ReadFile(operator)
	writeFile(t, defaultConfigPath(ta.home), string(data))
	if err := ta.run(context.Background(), reboot); err != nil {
		t.Errorf("with ~/.config/chalkos/config of an operator: %v", err)
	}
}

func TestClientFileExpiry(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	soon := ta.writeConfig(t, "alice", pki.RoleReader, 10*24*time.Hour, time.Now())
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", soon}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), "warning: the certificate of "+soon+" expires on") || !strings.Contains(ta.stderr.String(), "chalkctl config new") {
		t.Errorf("stderr = %q, want a warning naming chalkctl config new", ta.stderr)
	}

	old := newTestAppAt(t, time.Now().Add(-48*time.Hour))
	expired := old.writeConfig(t, "carol", pki.RoleReader, time.Hour, time.Now().Add(-47*time.Hour))
	err := old.run(context.Background(), old.args([]string{"status", "n1", "--config", expired}, addr))
	if err == nil || !strings.Contains(err.Error(), "the certificate of "+expired+" expired on") || !strings.Contains(err.Error(), "issue a new one with chalkctl config new") {
		t.Errorf("err = %v, want the expired client file named", err)
	}
}

func TestClientFileWithoutClusterDefinition(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	ta.withoutSecrets(t)
	// No --manifest and no flake.nix: the client file names the nodes.
	without := func(args ...string) []string {
		return append(args, "--flake", ta.dir, "--config", reader, "--endpoint", addr)
	}
	if err := ta.run(context.Background(), without("status", "n1")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "no cluster definition to compare it with") {
		t.Errorf("stdout = %q", ta.stdout)
	}
	if err := ta.run(context.Background(), without("status", "n9")); err == nil || !strings.Contains(err.Error(), "no node n9") {
		t.Errorf("an unknown node: %v", err)
	}
	if err := ta.run(context.Background(), without("storage", "reset", "n1", "var")); err == nil || !strings.Contains(err.Error(), "cluster definition") {
		t.Errorf("storage reset: %v, want a refusal naming the cluster definition", err)
	}
	if err := ta.run(context.Background(), []string{"etcd", "members", "--flake", ta.dir, "--config", reader}); err == nil || !strings.Contains(err.Error(), "--via") {
		t.Errorf("etcd members: %v, want a refusal naming --via", err)
	}

	// A client file of another cluster is refused where the cluster definition is at hand.
	other := ta.writeConfig(t, "dave", pki.RoleReader, time.Hour*24*365, time.Now())
	c, _ := client.ReadConfig(other)
	c.Cluster = "elsewhere"
	data, _ := c.Encode()
	writeFile(t, other, string(data))
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", other}, addr)); err == nil || !strings.Contains(err.Error(), "client file of the cluster elsewhere") {
		t.Errorf("another cluster's client file: %v", err)
	}
}

// The directory of a client file holds a private key: a missing one is created for its owner
// alone.
func TestConfigNewCreatesItsDirectoryPrivate(t *testing.T) {
	ta := newTestApp(t)
	for _, force := range []bool{false, true} {
		dir := filepath.Join(ta.dir, fmt.Sprintf("new-%v", force), "chalkos")
		args := []string{"config", "new", "--name", "alice", "--role", "reader", "--out", filepath.Join(dir, "config"), "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir}
		if force {
			args = append(args, "--force")
		}
		if err := ta.run(context.Background(), args); err != nil {
			t.Fatalf("force %v: %v", force, err)
		}
		for _, d := range []string{dir, filepath.Dir(dir)} {
			if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("force %v: %s: %v, %v; want mode 0700", force, d, info.Mode(), err)
			}
		}
	}
}

// Without a cluster definition, a node is reached at the address its client file names.
func TestClientFileNodes(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	c, err := client.NewConfig(ta.secrets.OSCA, ta.secrets.OSCABundle(), "lab", "alice", pki.RoleReader, time.Hour, map[string]string{"n1": addr}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ta.dir, "alice.json")
	writeFile(t, path, string(data))
	ta.withoutSecrets(t)
	if err := ta.run(context.Background(), []string{"status", "n1", "--config", path, "--flake", ta.dir}); err != nil {
		t.Fatalf("status through the client file's address: %v", err)
	}
	if !strings.Contains(ta.stdout.String(), "identity ") {
		t.Errorf("stdout = %q", ta.stdout)
	}
}

// writeAddressConfig writes a client file naming the nodes' addresses, which it prefers to the
// cluster definition's when prefer is set, and returns its path.
func (ta *testApp) writeAddressConfig(t *testing.T, nodes map[string]string, prefer bool) string {
	t.Helper()
	c, err := client.NewConfig(ta.secrets.OSCA, ta.secrets.OSCABundle(), "lab", "alice", pki.RoleReader, time.Hour, nodes, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.PreferNodeAddresses = prefer
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ta.dir, "alice.json")
	writeFile(t, path, string(data))
	return path
}

// TestClientFileAddresses reaches a node at the address a client file that prefers its addresses
// names, such as a forwarded port, before the static address of the cluster definition, and
// --endpoint before both. An address that cannot be reached is named with where it came from.
func TestClientFileAddresses(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	manifest := filepath.Join(ta.dir, "manifest.json")
	status := func(config string, extra ...string) error {
		return ta.run(context.Background(), append([]string{"status", "n1", "--config", config, "--manifest", manifest}, extra...))
	}
	if err := status(ta.writeAddressConfig(t, map[string]string{"n1": addr}, true)); err != nil {
		t.Fatalf("status at the client file's address: %v", err)
	}
	// Port 1 refuses the connection at once.
	closed := ta.writeAddressConfig(t, map[string]string{"n1": "127.0.0.1:1"}, true)
	if err := status(closed, "--endpoint", addr); err != nil {
		t.Errorf("--endpoint after a client file naming another address: %v", err)
	}
	err := status(closed)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1 (from the client file "+closed+")") {
		t.Errorf("an unreachable address of the client file: %v, want it named with the client file", err)
	}
	if err := status(closed, "--endpoint", "127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1 (from --endpoint)") {
		t.Errorf("an unreachable --endpoint: %v, want it named with --endpoint", err)
	}
}

// TestEndpointPrecedence picks --endpoint, then a client file's address when the file prefers
// its addresses and names the node, then the cluster definition's first static address.
func TestEndpointPrecedence(t *testing.T) {
	ta := newTestApp(t)
	def := &cluster{manifest: ta.manifest}
	file := func(nodes map[string]string, prefer bool) *credentials {
		return &credentials{config: &client.Config{Nodes: nodes, PreferNodeAddresses: prefer}, configPath: "lab.json"}
	}
	for _, tc := range []struct {
		name         string
		c            *cluster
		flag         string
		creds        *credentials
		addr, source string
	}{
		{"--endpoint beats the client file", def, "10.9.9.9", file(map[string]string{"n1": "127.0.0.1:15001"}, true), "10.9.9.9", "--endpoint"},
		{"the client file when it prefers its addresses", def, "", file(map[string]string{"n1": "127.0.0.1:15001"}, true), "127.0.0.1:15001", "the client file lab.json"},
		{"a node the client file does not name", def, "", file(map[string]string{"n2": "127.0.0.1:15002"}, true), "10.0.0.11", "the cluster definition"},
		{"the definition without the opt-in", def, "", file(map[string]string{"n1": "127.0.0.1:15001"}, false), "10.0.0.11", "the cluster definition"},
		{"the secrets file", def, "", &credentials{}, "10.0.0.11", "the cluster definition"},
		{"no cluster definition", clusterOfConfig(client.Config{Cluster: "lab", Nodes: map[string]string{"n1": "10.0.0.20"}}, clusterFlags{}), "", file(map[string]string{"n1": "10.0.0.20"}, false), "10.0.0.20", "the client file lab.json"},
		{"no cluster definition nor credentials", clusterOfConfig(client.Config{Cluster: "lab", Nodes: map[string]string{"n1": "10.0.0.20"}}, clusterFlags{}), "", nil, "10.0.0.20", "the client file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, source, err := tc.c.endpoint(tc.flag, tc.creds, "n1", tc.c.manifest.Nodes["n1"].Identity)
			if err != nil || addr != tc.addr || source != tc.source {
				t.Errorf("endpoint = %q, %q, %v; want %q, %q", addr, source, err, tc.addr, tc.source)
			}
		})
	}
}
