package chalkctl

import (
	"context"
	"encoding/json"
	"errors"
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
	c, err := client.NewConfig(ta.secrets.OSCA, ta.secrets.OSCABundle(), "lab", name, role, validity, now)
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

// withoutSecrets removes the secrets file from beside the inventory.
func (ta *testApp) withoutSecrets(t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join(ta.dir, "secrets.json")); err != nil {
		t.Fatal(err)
	}
}

func TestConfigNew(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "alice.json")
	args := []string{"config", "new", "--name", "alice", "--role", "operator", "--ttl", "720h", "--out", out, "--inventory", ta.inventory}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	c, err := client.ReadConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := c.Leaf()
	if c.Cluster != "lab" || c.Name != "alice" || c.Role != pki.RoleOperator || c.OSCA != ta.secrets.OSCA.Certificate {
		t.Errorf("client file %v", c)
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
	if err := ta.run(context.Background(), []string{"config", "new", "--name", "bob", "--role", "reader", "--inventory", ta.inventory}); err != nil {
		t.Fatal(err)
	}
	if c, err := client.ReadConfig(defaultConfigPath(ta.home)); err != nil || c.Name != "bob" {
		t.Errorf("default client file: %v, %v", c, err)
	}
	for _, bad := range [][]string{{"--name", "x", "--role", "root"}, {"--role", "reader"}, {"--name", "x", "--role", "node"}} {
		args := append([]string{"config", "new", "--out", filepath.Join(ta.dir, "bad.json"), "--inventory", ta.inventory}, bad...)
		if err := ta.run(context.Background(), args); err == nil {
			t.Errorf("config new %v: accepted", bad)
		}
	}
}

func TestClientFileRole(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.identity(t))
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
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1", "--config", reader}, addr)); err == nil {
		t.Error("apply-config ran with a client file")
	}
}

// TestCredentialChoice takes --config or --secrets, never both, else the client file
// $CHALKOSCONFIG names, else ~/.config/chalkos/config. A secrets file is read only where --secrets
// names it, also when one is at hand.
func TestCredentialChoice(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	secrets := filepath.Join(ta.dir, "secrets.json")
	t.Setenv("CHALKOSCONFIG", reader)
	// The app's own run names no secrets file of the test's.
	reboot := func(args ...string) error {
		return ta.app.run(context.Background(), append(ta.args([]string{"reboot", "n1"}, addr), args...))
	}

	if err := reboot(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("with $CHALKOSCONFIG: %v, want the reader refused", err)
	}
	// --secrets wins over $CHALKOSCONFIG.
	if err := reboot("--secrets", secrets); err != nil {
		t.Errorf("with --secrets: %v", err)
	}
	if err := reboot("--config", reader); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("with --config: %v, want the reader refused", err)
	}
	// Both would leave one unread, as --secrets - would standard input.
	if err := reboot("--config", reader, "--secrets", secrets); !errors.Is(err, errUsage) || !strings.Contains(err.Error(), "pass --secrets or --config, not both") {
		t.Errorf("with --config and --secrets: %v, want a usage error", err)
	}
	// Without $CHALKOSCONFIG, ~/.config/chalkos/config, else nothing: the secrets file in the
	// directory is not read.
	t.Setenv("CHALKOSCONFIG", "")
	if err := reboot(); err == nil || !strings.Contains(err.Error(), "--config") || !strings.Contains(err.Error(), "--secrets") {
		t.Errorf("without credentials: %v, want an error naming --config and --secrets", err)
	}
	operator := ta.writeConfig(t, "bob", pki.RoleOperator, time.Hour*24*365, time.Now())
	data, _ := os.ReadFile(operator)
	writeFile(t, defaultConfigPath(ta.home), string(data))
	if err := reboot(); err != nil {
		t.Errorf("with ~/.config/chalkos/config of an operator: %v", err)
	}
}

func TestClientFileExpiry(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.identity(t))
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

func TestClientFileWithoutAnInventory(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	ta.withoutSecrets(t)
	// No inventory: the client file names the cluster, and --address reaches the node.
	without := func(args ...string) []string {
		return append(args, "--config", reader, "--address", addr)
	}
	if err := ta.run(context.Background(), without("status", "n1")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "no inventory to compare it with") {
		t.Errorf("stdout = %q", ta.stdout)
	}
	if err := ta.run(context.Background(), without("status", "n9")); err == nil || !strings.Contains(err.Error(), "not n9") {
		t.Errorf("a node of another name: %v", err)
	}
	if err := ta.run(context.Background(), without("storage", "reset", "n1", "var")); err == nil || !strings.Contains(err.Error(), "inventory") {
		t.Errorf("storage reset: %v, want a refusal naming the inventory", err)
	}
	if err := ta.run(context.Background(), []string{"etcd", "members", "--config", reader}); err == nil || !strings.Contains(err.Error(), "--via") {
		t.Errorf("etcd members: %v, want a refusal naming --via", err)
	}

	// A client file of another cluster is refused where the inventory is at hand.
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
		args := []string{"config", "new", "--name", "alice", "--role", "reader", "--out", filepath.Join(dir, "config"), "--inventory", ta.inventory}
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

// TestUnreachableAddressNamesItsSource names an address chalkctl cannot reach with where it
// comes from: the inventory, or --address, which comes first.
func TestUnreachableAddressNamesItsSource(t *testing.T) {
	ta := newTestApp(t)
	// Port 1 refuses the connection at once.
	writeFile(t, ta.inventory, strings.Replace(testInventoryYAML, "address: 10.0.0.11", "address: 127.0.0.1:1", 1))
	err := ta.run(context.Background(), []string{"status", "n1", "--inventory", ta.inventory})
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1 (from the inventory "+ta.inventory+")") {
		t.Errorf("an unreachable address of the inventory: %v, want it named with the inventory", err)
	}
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), []string{"status", "n1", "--inventory", ta.inventory, "--address", addr}); err != nil {
		t.Errorf("--address before the inventory's: %v", err)
	}
	err = ta.run(context.Background(), []string{"status", "n1", "--inventory", ta.inventory, "--address", "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1 (from --address)") {
		t.Errorf("an unreachable --address: %v, want it named with --address", err)
	}
}
