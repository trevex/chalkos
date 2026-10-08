package client

import (
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
	c, err := NewConfig(osCA, osCA.Certificate, "lab", "alice", pki.RoleReader, 30*24*time.Hour, map[string]string{"n1": "10.0.0.11"}, now)
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
	if got.Name != "alice" || got.Role != pki.RoleReader || got.Nodes["n1"] != "10.0.0.11" || leaf.NotAfter.Sub(now).Round(time.Minute) != 30*24*time.Hour {
		t.Errorf("read back %v until %v", got, leaf.NotAfter)
	}
	if out := fmt.Sprintf("%v %+v %#v", got, got, got); strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("a formatted client file holds its key: %s", out)
	}
	if _, err := NewConfig(osCA, osCA.Certificate, "lab", "bob", pki.RoleNode, time.Hour, nil, now); err == nil {
		t.Error("issued a client file for the node role")
	}
}

func TestConfigValidate(t *testing.T) {
	now := time.Now()
	osCA, _ := pki.NewOSCA(now)
	nodeCA, _ := pki.NewNodeCA(osCA, now)
	other, _ := pki.NewOSCA(now)
	valid, err := NewConfig(osCA, osCA.Certificate, "lab", "alice", pki.RoleOperator, time.Hour, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	fromNodeCA, _ := pki.IssueLeaf(nodeCA, pki.Leaf{CommonName: "alice", Organization: []string{pki.RoleOperator}, Client: true}, now)
	otherKey, _ := pki.IssueClient(osCA, "alice", pki.RoleOperator, time.Hour, now)
	for name, edit := range map[string]func(c *Config){
		"another version": func(c *Config) { c.Version = 2 },
		"another role":    func(c *Config) { c.Role = pki.RoleAdmin },
		"another name":    func(c *Config) { c.Name = "bob" },
		"another OS CA":   func(c *Config) { c.OSCA = other.Certificate },
		"another key":     func(c *Config) { c.Key = otherKey.Key },
		"a certificate of the node CA": func(c *Config) {
			c.Certificate, c.Key = fromNodeCA.Certificate, fromNodeCA.Key
		},
		"no cluster": func(c *Config) { c.Cluster = "" },
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
	os.WriteFile(path, []byte(`{"version": 1, "extra": true}`), 0o600)
	if _, err := ReadConfig(path); err == nil {
		t.Error("read a client file with an unknown field")
	}
}
