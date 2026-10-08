package main

import (
	"bytes"
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
	ca, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(ca, now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCert, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, now)
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
		writeFile(t, filepath.Join(state, "chalkd", "node.pem"), nodeCert.Certificate+nodeCert.Key)
		writeFile(t, filepath.Join(state, "chalkd", "ca.crt"), ca.Certificate)
		c, err := loadCredentials(state, filepath.Join(root, "os-ca.crt"), filepath.Join(root, "run"), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.mode != nodev1.Mode_MODE_NORMAL || c.clientCAs == nil {
			t.Errorf("credentials = %+v, want normal mode", c)
		}
		if fp, err := c.fingerprint(); err != nil || fp != pki.Fingerprint(c.node.Current().Leaf.Raw) {
			t.Errorf("fingerprint = %q, %v", fp, err)
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

func TestHTTPServerLimits(t *testing.T) {
	s := httpServer(nil, nil)
	if s.ReadHeaderTimeout == 0 || s.IdleTimeout == 0 || s.MaxHeaderBytes == 0 {
		t.Errorf("server = %+v, want header and idle limits", s)
	}
	// Install and Logs stream for as long as they need.
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Errorf("read timeout %v, write timeout %v; want none", s.ReadTimeout, s.WriteTimeout)
	}
}

func TestMaintenanceCertificateAcrossRestarts(t *testing.T) {
	now := time.Now()
	load := func(t *testing.T, run string, now time.Time) []byte {
		t.Helper()
		root := filepath.Dir(run)
		c, err := loadCredentials(filepath.Join(root, "state"), filepath.Join(root, "os-ca.crt"), run, now)
		if err != nil {
			t.Fatal(err)
		}
		return c.maintenance.Certificate[0]
	}

	t.Run("reused", func(t *testing.T) {
		run := filepath.Join(t.TempDir(), "run")
		first := load(t, run, now)
		if second := load(t, run, now.Add(time.Hour)); !bytes.Equal(first, second) {
			t.Error("a restart changed the maintenance certificate")
		}
		for path, want := range map[string]os.FileMode{run: 0o700, filepath.Join(run, "maintenance.key"): 0o600} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s has mode %v, want %v", path, fi.Mode().Perm(), want)
			}
		}
	})
	for name, damage := range map[string]func(run string) error{
		"key missing":         func(run string) error { return os.Remove(filepath.Join(run, "maintenance.key")) },
		"certificate missing": func(run string) error { return os.Remove(filepath.Join(run, "maintenance.crt")) },
		"key corrupt": func(run string) error {
			return os.WriteFile(filepath.Join(run, "maintenance.key"), []byte("-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n"), 0o600)
		},
		"certificate corrupt": func(run string) error {
			return os.WriteFile(filepath.Join(run, "maintenance.crt"), []byte("garbage"), 0o644)
		},
	} {
		t.Run("regenerated when "+name, func(t *testing.T) {
			run := filepath.Join(t.TempDir(), "run")
			first := load(t, run, now)
			if err := damage(run); err != nil {
				t.Fatal(err)
			}
			second := load(t, run, now)
			if bytes.Equal(first, second) {
				t.Fatal("kept a maintenance certificate whose files are damaged")
			}
			// The new pair is on disk and is reused in turn.
			if third := load(t, run, now); !bytes.Equal(second, third) {
				t.Error("the regenerated certificate was not kept")
			}
		})
	}
	t.Run("regenerated when expired", func(t *testing.T) {
		run := filepath.Join(t.TempDir(), "run")
		first := load(t, run, now)
		if second := load(t, run, now.Add(pki.LeafValidity+time.Hour)); bytes.Equal(first, second) {
			t.Error("kept an expired maintenance certificate")
		}
	})
}
