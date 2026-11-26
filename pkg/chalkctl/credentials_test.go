package chalkctl

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
)

// writeConfig writes a client file of the test cluster for the role, valid for validity from
// now, and returns its path.
func (ta *testApp) writeConfig(t *testing.T, name string, role pki.Role, validity time.Duration, now time.Time) string {
	t.Helper()
	c, err := client.NewConfig(name, role, ta.secrets.OSCA, ta.secrets.OSCABundle(), validity, now)
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

// TestConfigNew issues a client file from the secrets file alone: it reads no inventory, and the
// file names no cluster.
func TestConfigNew(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "alice.json")
	args := []string{"client", "new", "--name", "alice", "--role", "operator", "--ttl", "720h", "--out", out}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	c, err := client.ReadConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := c.Leaf()
	if c.Name != "alice" || c.Role != pki.RoleOperator || c.OSCA != ta.secrets.OSCA.Certificate {
		t.Errorf("client file %v", c)
	}
	if got := time.Until(leaf.NotAfter).Round(time.Hour); got != 720*time.Hour {
		t.Errorf("the certificate lasts %v", got)
	}
	if data, _ := os.ReadFile(out); strings.Contains(string(data), `"cluster"`) {
		t.Errorf("the client file names a cluster: %s", data)
	}
	if err := ta.run(context.Background(), append(slices.Clone(args), "--inventory", ta.inventory)); err == nil || !strings.Contains(err.Error(), "unknown flag: --inventory") {
		t.Errorf("client new --inventory: %v, want the flag unknown", err)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	if err := ta.run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Errorf("err = %v, want a refusal to overwrite naming --overwrite", err)
	}
	if err := ta.run(context.Background(), append(args, "--overwrite")); err != nil {
		t.Errorf("--overwrite: %v", err)
	}
	// By default the file goes where chalkctl looks for it.
	if err := ta.run(context.Background(), []string{"client", "new", "--name", "bob", "--role", "reader"}); err != nil {
		t.Fatal(err)
	}
	if c, err := client.ReadConfig(defaultConfigPath(ta.home)); err != nil || c.Name != "bob" {
		t.Errorf("default client file: %v, %v", c, err)
	}
	// Without --ttl the certificate lasts as long as its role's default.
	for role, want := range map[string]time.Duration{"admin": 30 * 24 * time.Hour, "operator": 90 * 24 * time.Hour, "reader": 365 * 24 * time.Hour} {
		out := filepath.Join(ta.dir, role+".json")
		if err := ta.run(context.Background(), []string{"client", "new", "--name", "carol", "--role", role, "--out", out}); err != nil {
			t.Fatal(err)
		}
		c, err := client.ReadConfig(out)
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := c.Leaf()
		if got := time.Until(leaf.NotAfter).Round(time.Hour); got != want {
			t.Errorf("the default certificate of the %s role lasts %v, want %v", role, got, want)
		}
	}
	for _, bad := range [][]string{{"--name", "x", "--role", "root"}, {"--role", "reader"}, {"--name", "x", "--role", "node"}} {
		args := append([]string{"client", "new", "--out", filepath.Join(ta.dir, "bad.json")}, bad...)
		if err := ta.run(context.Background(), args); err == nil {
			t.Errorf("client new %v: accepted", bad)
		}
	}
}

func TestClientFileRole(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	ta.withoutSecrets(t)

	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", reader}, addr)); err != nil {
		t.Errorf("status as a reader: %v", err)
	}
	if err := ta.run(context.Background(), ta.args([]string{"node", "reboot", "n1", "--config", reader}, addr)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("reboot as a reader: %v, want permission denied", err)
	}
	// Commands that need the secrets file do not take a client file.
	if err := ta.run(context.Background(), ta.args([]string{"config", "apply", "n1", "--config", reader}, addr)); err == nil {
		t.Error("config apply ran with a client file")
	}
}

// TestCredentialChoice takes --config or --secrets, never both, else the client file
// $CHALKOSCONFIG names, else ~/.config/chalkos/config. A secrets file is read only where --secrets
// names it, also when one is at hand.
func TestCredentialChoice(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	secrets := filepath.Join(ta.dir, "secrets.json")
	t.Setenv("CHALKOSCONFIG", reader)
	// The app's own run names no secrets file of the test's.
	reboot := func(args ...string) error {
		return ta.app.run(context.Background(), append(ta.args([]string{"node", "reboot", "n1"}, addr), args...))
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
	if err := reboot("--config", reader, "--secrets", secrets); !errors.Is(err, cli.ErrUsage) || !strings.Contains(err.Error(), "pass --secrets or --config, not both") {
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
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	soon := ta.writeConfig(t, "alice", pki.RoleReader, 10*24*time.Hour, time.Now())
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", soon}, addr)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ta.stderr.String(), "warning") {
		t.Errorf("stderr = %q, want no warning for a certificate in the first third of its lifetime", ta.stderr)
	}

	old := newTestAppAt(t, time.Now().Add(-48*time.Hour))
	oldNode, _ := installedNode(t, old, nil)
	oldAddr := old.startNode(t, oldNode)
	// A 30-hour certificate with 9 hours left is in the last third of its lifetime.
	soon = old.writeConfig(t, "alice", pki.RoleReader, 30*time.Hour, time.Now().Add(-21*time.Hour))
	if err := old.run(context.Background(), old.args([]string{"status", "n1", "--config", soon}, oldAddr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(old.stderr.String(), "warning: the certificate of "+soon+" expires on") || !strings.Contains(old.stderr.String(), "chalkctl client new") {
		t.Errorf("stderr = %q, want a warning naming chalkctl client new", old.stderr)
	}

	expired := old.writeConfig(t, "carol", pki.RoleReader, time.Hour, time.Now().Add(-47*time.Hour))
	err := old.run(context.Background(), old.args([]string{"status", "n1", "--config", expired}, addr))
	if err == nil || !strings.Contains(err.Error(), "the certificate of "+expired+" expired on") || !strings.Contains(err.Error(), "issue a new one with chalkctl client new") {
		t.Errorf("err = %v, want the expired client file named", err)
	}
}

// TestExpiresSoon warns in the last third of a certificate's lifetime: the last 8 hours of a day's,
// the last 4 months of a year's.
func TestExpiresSoon(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		validity, age time.Duration
		want          bool
	}{
		{24 * time.Hour, 15 * time.Hour, false},
		{24 * time.Hour, 17 * time.Hour, true},
		{365 * 24 * time.Hour, 240 * 24 * time.Hour, false},
		{365 * 24 * time.Hour, 245 * 24 * time.Hour, true},
	} {
		leaf := &x509.Certificate{NotBefore: issued, NotAfter: issued.Add(c.validity)}
		if got := expiresSoon(leaf, issued.Add(c.age)); got != c.want {
			t.Errorf("expiresSoon of a %v certificate after %v = %v, want %v", c.validity, c.age, got, c.want)
		}
	}
}

func TestClientFileWithoutAnInventory(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	ta.withoutSecrets(t)
	// No inventory: --address reaches the node, and its certificate must name the node.
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
}

// TestClientFileOfAnotherCluster names the client file and the side that failed when a TLS
// handshake fails for want of trust: the OS CA of another cluster's client file does not verify
// the node, and a node refuses a certificate its OS CA did not issue. Both fail every call, unary
// and streaming alike, with or without an inventory.
func TestClientFileOfAnotherCluster(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	foreign, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	clientFile := func(file, trusted string) string {
		c, err := client.NewConfig("dave", pki.RoleAdmin, foreign.OSCA, trusted, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		data, _ := c.Encode()
		path := filepath.Join(ta.dir, file)
		writeFile(t, path, string(data))
		return path
	}
	// A client file of another cluster, and one whose certificate another OS CA issued, though
	// it verifies the node.
	other := clientFile("other.json", foreign.OSCABundle())
	refused := clientFile("refused.json", pki.Bundle(foreign.OSCA.Certificate, ta.secrets.OSCA.Certificate))
	ta.withoutSecrets(t)

	for path, want := range map[string]string{
		other:   "this client file's OS CA does not verify the node's certificate, " + other + ": is the file another cluster's or from before an OS CA rotation, or does the address reach another cluster's node? (",
		refused: "the node does not accept this client file, " + refused + "; is it another cluster's? (",
	} {
		for _, args := range [][]string{
			ta.args([]string{"status", "n1", "--config", path}, addr),
			{"status", "n1", "--config", path, "--address", addr},
			ta.args([]string{"logs", "n1", "--config", path}, addr),
		} {
			if err := ta.run(context.Background(), args); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v, want %q", strings.Join(args, " "), err, want)
			}
		}
	}
	reader := ta.writeConfig(t, "alice", pki.RoleReader, time.Hour*24*365, time.Now())
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", reader}, addr)); err != nil {
		t.Errorf("status with the cluster's client file: %v", err)
	}
}

// The directory of a client file holds a private key: a missing one is created for its owner
// alone.
func TestConfigNewCreatesItsDirectoryPrivate(t *testing.T) {
	ta := newTestApp(t)
	dir := filepath.Join(ta.dir, "new", "chalkos")
	if err := ta.run(context.Background(), []string{"client", "new", "--name", "alice", "--role", "reader", "--out", filepath.Join(dir, "config")}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want mode 0700", d, info.Mode(), err)
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

// The admin certificate chalkctl issues itself from the secrets file is chalkctl's and lasts an
// hour at most.
func TestAdminCertificateLastsOneRun(t *testing.T) {
	ta := newTestApp(t)
	c, err := secretCredentials(ta.secrets)
	if err != nil {
		t.Fatal(err)
	}
	cert := c.cert.Leaf
	if cert == nil {
		if cert, err = x509.ParseCertificate(c.cert.Certificate[0]); err != nil {
			t.Fatal(err)
		}
	}
	if cert.Subject.CommonName != "chalkctl" || time.Until(cert.NotAfter) > time.Hour {
		t.Errorf("the admin certificate is %s until %v, want chalkctl for an hour at most", cert.Subject, cert.NotAfter)
	}
}

// TestCredentialsRole grants the secrets file admin and a client file the role it names.
func TestCredentialsRole(t *testing.T) {
	ta := newTestApp(t)
	c, err := client.NewConfig("alice", pki.RoleReader, ta.secrets.OSCA, ta.secrets.OSCABundle(), time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := (&credentials{secrets: &ta.secrets}).role(); got != pki.RoleAdmin {
		t.Errorf("the secrets file grants %q, want admin", got)
	}
	if got := (&credentials{config: &c}).role(); got != pki.RoleReader {
		t.Errorf("a reader's client file grants %q", got)
	}
}

// A command that reaches one node takes its address as NODE=ADDR too, and refuses a NODE=ADDR
// of a node the cluster does not have.
func TestNamedAddressOfOneNode(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), []string{"status", "n1", "--inventory", ta.inventory, "--address", "n1=" + addr}); err != nil {
		t.Errorf("--address n1=ADDR: %v", err)
	}
	err := ta.run(context.Background(), []string{"status", "n1", "--inventory", ta.inventory, "--address", "n9=" + addr})
	if err == nil || !strings.Contains(err.Error(), "has no node n9") {
		t.Errorf("--address n9=ADDR: %v, want the node refused", err)
	}
}
