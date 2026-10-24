package chalkctl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledNodeRequiresOSCA(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatalf("the node does not accept this cluster's admin: %v", err)
	}

	// This cluster's admin certificate, which the node accepts, but another cluster's OS CA:
	// chalkctl itself must refuse the node's certificate.
	other := ta.secrets
	other.OSCA = newTestApp(t).secrets.OSCA
	data, _ := other.Encode()
	otherSecrets := filepath.Join(ta.dir, "other", "secrets.json")
	writeFile(t, otherSecrets, string(data))
	err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--secrets", otherSecrets}, addr))
	if err == nil || !strings.Contains(err.Error(), "x509") || !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("err = %v, want the node's certificate refused as signed by an unknown authority", err)
	}

	// The node's certificate names n1, so it cannot pass for n2.
	writeFile(t, ta.inventory, testInventoryYAML+"    n2: {address: 10.0.0.11, files: [nodes/n2.yaml]}\n")
	writeFile(t, filepath.Join(ta.dir, "nodes", "n2.yaml"), strings.Join([]string{n1SystemDisk, n1Network}, "---\n"))
	err = ta.run(context.Background(), ta.args([]string{"status", "n2"}, addr))
	if err == nil || !strings.Contains(err.Error(), "x509") || !strings.Contains(err.Error(), "not n2") {
		t.Fatalf("err = %v, want the node's certificate refused for the name n2", err)
	}
}

// TestAddressWithABrokenConfig reaches a node whose config chalkctl cannot read at the address
// the inventory names, so commands still reach it.
func TestAddressWithABrokenConfig(t *testing.T) {
	ta := newTestApp(t)
	ta.writeNode(t, n1SystemDisk, n1SystemDisk, n1Network)
	c, err := loadInventory(context.Background(), ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	if addr, _, err := c.address("", "n1"); err != nil || addr != "10.0.0.11" {
		t.Errorf("address = %q, %v; want the inventory's 10.0.0.11", addr, err)
	}
}
