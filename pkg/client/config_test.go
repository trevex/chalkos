package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

func TestConfigRoundTrip(t *testing.T) {
	now := time.Now()
	osCA, _ := pki.NewOSCA(now)
	c, err := NewConfig("alice", pki.RoleReader, osCA, osCA.Certificate, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := got.Leaf()
	if got.Name != "alice" || got.Role != pki.RoleReader || leaf.NotAfter.Sub(now).Round(time.Minute) != 30*24*time.Hour {
		t.Errorf("read back %v until %v", got, leaf.NotAfter)
	}
	if out := fmt.Sprintf("%v %+v %#v", got, got, got); strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("a formatted client file holds its key: %s", out)
	}
	if _, err := NewConfig("bob", pki.RoleNode, osCA, osCA.Certificate, time.Hour, now); err == nil {
		t.Error("issued a client file for the node role")
	}
}

func TestConfigValidate(t *testing.T) {
	now := time.Now()
	osCA, _ := pki.NewOSCA(now)
	nodeCA, _ := pki.NewNodeCA(osCA, now)
	other, _ := pki.NewOSCA(now)
	valid, err := NewConfig("alice", pki.RoleOperator, osCA, osCA.Certificate, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	fromNodeCA, _ := pki.IssueLeaf(nodeCA, pki.Leaf{CommonName: "alice", Organization: []string{string(pki.RoleOperator)}, Client: true}, now)
	otherKey, _ := pki.IssueClient(osCA, "alice", pki.RoleOperator, time.Hour, now)
	for name, edit := range map[string]func(c *Config){
		"another version": func(c *Config) { c.Version = 1 },
		"another role":    func(c *Config) { c.Role = pki.RoleAdmin },
		"another name":    func(c *Config) { c.Name = "bob" },
		"another OS CA":   func(c *Config) { c.OSCA = other.Certificate },
		"another key":     func(c *Config) { c.Key = otherKey.Key },
		"a certificate of the node CA": func(c *Config) {
			c.Certificate, c.Key = fromNodeCA.Certificate, fromNodeCA.Key
		},
		"no name": func(c *Config) { c.Name = "" },
	} {
		c := valid
		edit(&c)
		err := c.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: the error holds the key", name)
		}
	}
	path := filepath.Join(t.TempDir(), "config")
	os.WriteFile(path, []byte(`{"version": 3, "extra": true}`), 0o600)
	if _, err := ReadConfig(path); err == nil {
		t.Error("read a client file with an unknown field")
	}
}

// TestConfigOfVersion1 refuses a client file of the version that named the nodes, saying how to
// replace it.
func TestConfigOfVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	os.WriteFile(path, []byte(`{"version": 1, "cluster": "lab", "name": "alice", "role": "reader", "certificate": "", "key": "", "osCA": "", "nodes": {"n1": "10.0.0.11"}}`), 0o600)
	if _, err := ReadConfig(path); err == nil || !strings.Contains(err.Error(), "version 1 is not supported (want 3); issue a new one with chalkctl config new") {
		t.Errorf("a client file of version 1: %v", err)
	}
}

// TestConfigOfVersion2 refuses a client file of the version that named the cluster, saying how
// to replace it, rather than refusing its cluster as an unknown field.
func TestConfigOfVersion2(t *testing.T) {
	now := time.Now()
	osCA, _ := pki.NewOSCA(now)
	c, err := NewConfig("alice", pki.RoleReader, osCA, osCA.Certificate, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"version": 2, "cluster": "lab", "name": c.Name, "role": c.Role, "certificate": c.Certificate, "key": c.Key, "osCA": c.OSCA})
	path := filepath.Join(t.TempDir(), "config")
	os.WriteFile(path, data, 0o600)
	if _, err := ReadConfig(path); err == nil || !strings.Contains(err.Error(), "version 2 is not supported (want 3); issue a new one with chalkctl config new") {
		t.Errorf("a client file of version 2: %v", err)
	}
	if data, _ := c.Encode(); strings.Contains(string(data), "cluster") {
		t.Errorf("a client file names a cluster: %s", data)
	}
}

// TestConfigNamesTheUser refuses a client file without the user's name.
func TestConfigNamesTheUser(t *testing.T) {
	now := time.Now()
	osCA, _ := pki.NewOSCA(now)
	c, err := NewConfig("alice", pki.RoleReader, osCA, osCA.Certificate, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	c.Name = ""
	if err := c.Validate(); err == nil || err.Error() != "a client file names the user" {
		t.Errorf("a client file without a name: %v", err)
	}
}
