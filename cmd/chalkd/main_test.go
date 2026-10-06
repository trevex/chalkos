package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/pki"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCredentials(t *testing.T) {
	now := time.Now()
	ca, err := pki.NewCA("chalkos OS CA", now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCert, err := pki.IssueNode(ca, "n1", []string{"n1"}, nil, now)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("maintenance on a generic image", func(t *testing.T) {
		root := t.TempDir()
		c, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_MAINTENANCE || c.clientCAs != nil {
			t.Errorf("credentials = %+v, want maintenance accepting any client", c)
		}
		if _, err := os.Stat(filepath.Join(root, "run", "maintenance.crt")); err != nil {
			t.Error("the maintenance certificate was not written")
		}
	})
	t.Run("maintenance on an image with an OS CA", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "os-ca.crt"), ca.Certificate)
		c, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_MAINTENANCE || c.clientCAs == nil {
			t.Errorf("credentials = %+v, want maintenance requiring clients of the OS CA", c)
		}
	})
	t.Run("normal", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		writeFile(t, filepath.Join(state, "installed"), "")
		writeFile(t, filepath.Join(state, "chalkd", "node.crt"), nodeCert.Certificate)
		writeFile(t, filepath.Join(state, "chalkd", "node.key"), nodeCert.Key)
		writeFile(t, filepath.Join(state, "chalkd", "ca.crt"), ca.Certificate)
		c, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_NORMAL || c.clientCAs == nil {
			t.Errorf("credentials = %+v, want normal mode", c)
		}
	})
	t.Run("installed without its certificate", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		writeFile(t, filepath.Join(state, "installed"), "")
		if _, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now); err == nil {
			t.Error("an installed node without its certificate fell back to maintenance mode")
		}
	})
	t.Run("unreadable state fails closed", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		// A state directory without execute permission makes Stat of a file inside it fail with
		// something other than "does not exist", which must not be treated as "not installed".
		if err := os.Chmod(state, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(state, 0o755)
		if _, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now); err == nil {
			t.Error("an unreadable state directory fell back to maintenance mode instead of failing")
		}
	})
}
